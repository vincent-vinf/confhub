package storage_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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
