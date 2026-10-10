package storage_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/storage"
	"gitlab.bodesitech.com/bodesi/confhub/internal/testutil"
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

func TestSchemaTwoRequiresExplicitPresenceUpgradeAndPreservesConfig(t *testing.T) {
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
	// Model schema 2 without changing configuration tables or their data.
	for _, q := range []string{"DROP TABLE client_tags", "DROP TABLE connected_clients", "DROP TABLE client_instances", "UPDATE schema_migrations SET version=2"} {
		if _, err = setup.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = storage.Migrate("postgres", dsn, true); err == nil || !strings.Contains(err.Error(), "run confhub migrate") {
		t.Fatal(err)
	}
	if err = storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.Current(ctx, k)
	if err != nil || current.Versions[1].Content != "keep" {
		t.Fatal(current, err)
	}
	if _, err = s.Clients(ctx, "", 25); err != nil {
		t.Fatal(err)
	}
}
