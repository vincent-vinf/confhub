package storage_test

import (
	"context"
	"database/sql"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"os"
	"strings"
	"testing"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestMigrationIsIdempotentAndDoesNotCloseApplicationPool(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, true); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = storage.Migrate("postgres", dsn, true); err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Ping(context.Background()); err != nil {
		t.Fatalf("migration closed business pool: %v", err)
	}
	if err = s.InitializeAdmin(context.Background(), ""); err == nil {
		t.Fatal("initialized admin without a password")
	}
}
func TestDirtySchemaIsNeverAutomaticallyForced(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	setup, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	// External fault injection models a migration interrupted after setting dirty.
	if _, err = setup.Exec("UPDATE schema_migrations SET dirty=true"); err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{true, false} {
		err = storage.Migrate("postgres", dsn, initialize)
		if err == nil || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("dirty schema accepted: %v", err)
		}
	}
}

func TestLegacySchemaRequiresExplicitDatabaseRecreation(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	setup, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	// Model an old deployment's schema marker without touching a real database.
	if _, err = setup.Exec("UPDATE schema_migrations SET version=1"); err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{true, false} {
		err = storage.Migrate("postgres", dsn, initialize)
		if err == nil || !strings.Contains(err.Error(), "recreate an empty database") {
			t.Fatalf("legacy schema was accepted: %v", err)
		}
	}
}

func TestAllPriorSchemasRequireRecreationWithoutMutatingConfig(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			dsn := testutil.Database(t)
			if err := storage.Migrate("postgres", dsn, false); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			s, err := storage.Open(ctx, "postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "preserved"}
			if _, err = s.Save(ctx, k, config.Edit{Content: "keep", Format: "text"}); err != nil {
				t.Fatal(err)
			}
			setup, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer setup.Close()
			if _, err = setup.Exec("UPDATE schema_migrations SET version=$1", version); err != nil {
				t.Fatal(err)
			}
			for _, initialize := range []bool{true, false} {
				if err = storage.Migrate("postgres", dsn, initialize); err == nil || !strings.Contains(err.Error(), "recreate an empty database") {
					t.Fatal(err)
				}
			}
			current, _, err := s.Current(ctx, k)
			if err != nil || current.Versions[1].Content != "keep" {
				t.Fatal(current, err)
			}
		})
	}
}

func TestSchemaFourUpgradePreservesAdminAndConfigurations(t *testing.T) {
	dsn := testutil.Database(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, file := range []string{"000001_initial.up.sql", "000002_mutable_beta.up.sql", "000003_client_presence.up.sql", "000004_shared_beta.up.sql"} {
		contents, err := os.ReadFile("migrations/postgres/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(contents)); err != nil {
			t.Fatal(file, err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO admin VALUES (1,$1)", string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, dirty BOOLEAN NOT NULL); INSERT INTO schema_migrations VALUES (4,FALSE)"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "preserved"}
	m, err := store.Save(ctx, key, config.Edit{Content: "keep", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate("postgres", dsn, true); err == nil || !strings.Contains(err.Error(), "run confhub migrate") {
		t.Fatal("implicit upgrade", err)
	}
	if err = storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	if err = store.InitializeAdmin(ctx, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	u, err := store.Authenticate(ctx, "admin", "original-password")
	if err != nil || !u.SystemAdmin {
		t.Fatal(u, err)
	}
	current, err := store.Snapshot(ctx, key)
	if err != nil || current.ID != m.State.ID || current.Versions[1].Content != "keep" {
		t.Fatal(current, err)
	}
	if err = storage.Migrate("postgres", dsn, true); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaFiveAuditUpgradePreservesUsersAndConfigurations(t *testing.T) {
	dsn := testutil.Database(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, file := range []string{"000001_initial.up.sql", "000002_mutable_beta.up.sql", "000003_client_presence.up.sql", "000004_shared_beta.up.sql", "000005_users.up.sql"} {
		contents, err := os.ReadFile("migrations/postgres/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(contents)); err != nil {
			t.Fatal(file, err)
		}
	}
	if _, err = db.Exec("CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, dirty BOOLEAN NOT NULL); INSERT INTO schema_migrations VALUES (5,FALSE)"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	u, err := store.CreateUser(ctx, "operator", "operator-password", storage.UserInput{Enabled: true, Grants: []storage.Grant{{Namespace: "public", Access: "write"}}})
	if err != nil {
		t.Fatal(err)
	}
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "preserved"}
	m, err := store.Save(ctx, key, config.Edit{Content: "keep", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if err = storage.Migrate("postgres", dsn, true); err == nil || !strings.Contains(err.Error(), "run confhub migrate") {
		t.Fatal("implicit upgrade", err)
	}
	if err = storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	got, err := store.Authenticate(ctx, "operator", "operator-password")
	if err != nil || got.ID != u.ID || !got.Allows("config.write", "public", "DEFAULT_GROUP") {
		t.Fatal(got, err)
	}
	current, err := store.Snapshot(ctx, key)
	if err != nil || current.ID != m.State.ID || current.Versions[1].Content != "keep" {
		t.Fatal(current, err)
	}
	page, err := store.Audits(ctx, storage.AuditFilter{Limit: 25})
	if err != nil || len(page.Audits) != 0 {
		t.Fatal(page, err)
	}
	if err = storage.Migrate("postgres", dsn, true); err != nil {
		t.Fatal(err)
	}
}
