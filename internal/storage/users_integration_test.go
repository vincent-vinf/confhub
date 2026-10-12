package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestUsersLifecycleAndConcurrentUpdates(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	if err := s.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	admin, err := s.Authenticate(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []storage.UserInput{{Enabled: false, SystemAdmin: true}, {Enabled: true, SystemAdmin: false}} {
		if _, err = s.UpdateUser(ctx, admin.ID, admin.Revision, input); !errors.Is(err, storage.ErrForbidden) {
			t.Fatal("admin demoted", err)
		}
	}
	if err = s.DeleteUser(ctx, admin.ID, admin.Revision); !errors.Is(err, storage.ErrForbidden) {
		t.Fatal("admin deleted", err)
	}
	input := storage.UserInput{Enabled: true, Grants: []storage.Grant{{Namespace: "public", Access: "read"}}}
	u, err := s.CreateUser(ctx, "reader", "initial-password", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateUser(ctx, "reader", "initial-password", input); !errors.Is(err, config.ErrConflict) {
		t.Fatal("duplicate", err)
	}
	bad := []storage.UserInput{
		{Grants: []storage.Grant{{Access: "admin"}}},
		{Grants: []storage.Grant{{Group: "group", Access: "read"}}},
		{Grants: []storage.Grant{{Namespace: "public", Access: "read"}, {Namespace: "public", Access: "write"}}},
	}
	for _, b := range bad {
		if _, err = s.UpdateUser(ctx, u.ID, u.Revision, b); !errors.Is(err, config.ErrInvalid) {
			t.Fatal("invalid grants", err)
		}
	}
	for _, tc := range [][2]string{{"bad user", "initial-password"}, {"short", "123"}} {
		if _, err = s.CreateUser(ctx, tc[0], tc[1], input); !errors.Is(err, config.ErrInvalid) {
			t.Fatal("validation", err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.UpdateUser(ctx, u.ID, u.Revision, input); results <- err }()
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, config.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	u, err = s.User(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResetPassword(ctx, u.ID, u.Revision, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, "reader", "initial-password"); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("old password", err)
	}
	updated, err := s.Authenticate(ctx, "reader", "replacement-password")
	if err != nil || updated.AuthVersion <= u.AuthVersion {
		t.Fatal(updated, err)
	}
	if err = s.ChangePassword(ctx, updated.ID, updated.AuthVersion, "wrong-password", "new-password"); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, updated.ID, updated.AuthVersion, "replacement-password", "new-password"); err != nil {
		t.Fatal(err)
	}
	updated, err = s.User(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := s.UpdateUser(ctx, u.ID, updated.Revision, storage.UserInput{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authenticate(ctx, "reader", "new-password"); !errors.Is(err, storage.ErrUnauthorized) {
		t.Fatal("disabled logged in", err)
	}
	if err = s.DeleteUser(ctx, u.ID, disabled.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.User(ctx, u.ID); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("deleted", err)
	}
	users, err := s.Users(ctx, "", 100)
	if err != nil || len(users) != 1 || users[0].Username != "admin" {
		t.Fatal(users, err)
	}
}
