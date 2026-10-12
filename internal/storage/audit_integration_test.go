package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestAuditLifecycleFiltersAndSnapshots(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	user, err := s.CreateUser(ctx, "operator", "operator-password", storage.UserInput{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 12, 12, 0, 0, 123456000, time.UTC)
	a := storage.Audit{RequestID: uuid.NewString(), CreatedAt: when, IP: "192.168.2.1", Action: "config.publish", Subject: "public / DEFAULT_GROUP / app", Method: "PUT"}
	a.ID, err = s.BeginAudit(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.IdentifyAudit(ctx, a.ID, user); err != nil {
		t.Fatal(err)
	}
	page, err := s.Audits(ctx, storage.AuditFilter{Limit: 25, Result: "pending"})
	if err != nil || len(page.Audits) != 1 || page.Audits[0].Username != user.Username || page.Audits[0].Status != 0 {
		t.Fatal(page, err)
	}
	a.UserID, a.Username, a.Result, a.Status = user.ID, user.Username, "success", 200
	a.Details = json.RawMessage(`{"global_version":2}`)
	if err = s.FinishAudit(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishAudit(ctx, a); !errors.Is(err, config.ErrConflict) {
		t.Fatal("completed record overwritten", err)
	}
	if err = s.IdentifyAudit(ctx, a.ID, user); !errors.Is(err, config.ErrConflict) {
		t.Fatal("completed actor overwritten", err)
	}
	if err = s.DeleteUser(ctx, user.ID, user.Revision); err != nil {
		t.Fatal(err)
	}
	f := storage.AuditFilter{Limit: 1, Username: "operator", IP: "::ffff:192.168.2.1", Action: "config.publish", Result: "success", From: when, To: when.Add(time.Microsecond)}
	page, err = s.Audits(ctx, f)
	if err != nil || len(page.Audits) != 1 || !page.Audits[0].CreatedAt.Equal(when) || page.Audits[0].UserID != user.ID || string(page.Audits[0].Details) != string(a.Details) {
		t.Fatal(page, err)
	}
	raw, _ := json.Marshal(page)
	if !strings.Contains(string(raw), fmt.Sprintf(`"id":"%d"`, a.ID)) {
		t.Fatal("ID must remain precise in JavaScript", string(raw))
	}
	f.To = when
	page, err = s.Audits(ctx, f)
	if err != nil || len(page.Audits) != 0 {
		t.Fatal("end must be exclusive", page, err)
	}
	f.From, f.To = when.Add(time.Microsecond), time.Time{}
	page, err = s.Audits(ctx, f)
	if err != nil || len(page.Audits) != 0 {
		t.Fatal(page, err)
	}
	failed := storage.Audit{RequestID: uuid.NewString(), CreatedAt: when, IP: "::1", Action: "auth.login", Method: "POST"}
	failed.ID, err = s.BeginAudit(ctx, failed)
	if err != nil {
		t.Fatal(err)
	}
	failed.Result, failed.Status, failed.AttemptedUsername = "failure", 401, "operator"
	if err = s.FinishAudit(ctx, failed); err != nil {
		t.Fatal(err)
	}
	page, err = s.Audits(ctx, storage.AuditFilter{Limit: 25, Username: "operator"})
	if err != nil || len(page.Audits) != 2 || page.Audits[0].Username != "" || page.Audits[0].AttemptedUsername != "operator" {
		t.Fatal(page, err)
	}
	for _, invalid := range []storage.AuditFilter{
		{Limit: 0}, {Limit: 101}, {Limit: 1, Before: -1}, {Limit: 1, IP: "garbage"}, {Limit: 1, IP: "fe80::1%eth0"}, {Limit: 1, Result: "unknown"}, {Limit: 1, Username: strings.Repeat("x", 65)}, {Limit: 1, Action: strings.Repeat("x", 65)}, {Limit: 1, From: when, To: when.Add(-time.Second)},
	} {
		if _, err = s.Audits(ctx, invalid); !errors.Is(err, config.ErrInvalid) {
			t.Fatal(invalid, err)
		}
	}
}

func TestAuditConcurrentInsertAndStableCursor(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := storage.Audit{RequestID: uuid.NewString(), CreatedAt: time.Now(), IP: "127.0.0.1", Action: "config.publish", Method: "PUT"}
			var err error
			a.ID, err = s.BeginAudit(ctx, a)
			if err == nil {
				a.Result, a.Status = "success", 200
				err = s.FinishAudit(ctx, a)
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	f := storage.AuditFilter{Limit: 3}
	seen := map[int64]bool{}
	for {
		page, err := s.Audits(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Audits {
			if seen[a.ID] || a.Result != "success" || (f.Before > 0 && a.ID >= f.Before) {
				t.Fatal(a)
			}
			seen[a.ID] = true
		}
		if len(seen) == 3 {
			// New records between page requests must not duplicate or shift older pages.
			if _, err := s.BeginAudit(ctx, storage.Audit{RequestID: uuid.NewString(), CreatedAt: time.Now(), IP: "127.0.0.1", Action: "auth.login", Method: "POST"}); err != nil {
				t.Fatal(err)
			}
		}
		if page.NextBefore == "" {
			break
		}
		f.Before, err = strconv.ParseInt(page.NextBefore, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 16 {
		t.Fatal("lost audit records", len(seen))
	}
}
