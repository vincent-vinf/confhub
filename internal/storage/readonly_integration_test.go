package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestReadOnlyStoreRejectsManagementAndBackgroundWrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "preserved", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.Authenticate(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	s.DisableWrites()
	targets := []storage.ConfigTarget{{Key: k, ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision}}
	for name, action := range map[string]func() error{
		"batch-delete": func() error { _, e := s.DeleteMany(ctx, targets); return e },
		"batch-move":   func() error { _, e := s.MoveMany(ctx, targets, "public", "target"); return e },
		"save": func() error {
			_, e := s.Save(ctx, k, config.Edit{Content: "changed", Format: "text", ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision})
			return e
		},
		"delete":       func() error { _, e := s.Delete(ctx, k, m.State.ID, m.State.Revision); return e },
		"rules":        func() error { _, e := s.SetRules(ctx, k, m.State.ID, m.State.Revision, nil, 0); return e },
		"rollback":     func() error { _, e := s.CopyVersion(ctx, k, m.State.ID, m.State.Revision, 1, "global", true); return e },
		"namespace":    func() error { return s.CreateNamespace(ctx, "blocked") },
		"group":        func() error { return s.CreateGroup(ctx, "public", "blocked") },
		"delete-group": func() error { return s.DeleteGroup(ctx, "public", "DEFAULT_GROUP") },
		"admin":        func() error { return s.InitializeAdmin(ctx, "replacement-password") },
		"password": func() error {
			return s.ChangePassword(ctx, u.ID, u.AuthVersion, "initial-password", "replacement-password")
		},
		"reset-password": func() error { return s.ResetPassword(ctx, u.ID, u.Revision, "replacement-password") },
		"create-user": func() error {
			_, e := s.CreateUser(ctx, "blocked", "valid-password", storage.UserInput{Enabled: true})
			return e
		},
		"update-user": func() error {
			_, e := s.UpdateUser(ctx, u.ID, u.Revision, storage.UserInput{Enabled: true, SystemAdmin: true})
			return e
		},
		"delete-user":      func() error { return s.DeleteUser(ctx, u.ID, u.Revision) },
		"cleanup":          func() error { _, e := s.Cleanup(ctx, "owner", 1, time.Second, 1); return e },
		"presence":         func() error { return s.SyncPresence(ctx, "owner", nil, time.Second) },
		"remove-presence":  func() error { return s.RemovePresence(ctx, "owner") },
		"cleanup-presence": func() error { return s.CleanupPresence(ctx) },
		"begin-audit":      func() error { _, e := s.BeginAudit(ctx, storage.Audit{}); return e },
		"identify-audit":   func() error { return s.IdentifyAudit(ctx, 1, u) },
		"finish-audit":     func() error { return s.FinishAudit(ctx, storage.Audit{ID: 1}) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := action(); !errors.Is(err, storage.ErrReadOnly) {
				t.Fatalf("write returned %v", err)
			}
		})
	}
	state, _, err := s.Current(ctx, k)
	if err != nil || config.Resolve(state, nil).Content != "preserved" {
		t.Fatal(state, err)
	}
}

func TestSchemaCheckUsesSelectOnlyAndRejectsIncompleteSchemas(t *testing.T) {
	ctx := context.Background()
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	reader, err := storage.Open(ctx, "postgres", testutil.ReadOnlyDSN(t, dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range []struct{ sql, message string }{
		{"UPDATE schema_migrations SET dirty=true", "dirty"},
		{"UPDATE schema_migrations SET dirty=false,version=5", "expected 6"},
		{"DELETE FROM schema_migrations", "read schema version"},
		{"DROP TABLE schema_migrations", "read schema version"},
	} {
		if _, err := db.Exec(tc.sql); err != nil {
			t.Fatal(err)
		}
		if err := reader.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Fatal("invalid schema accepted", err)
		}
	}
}
