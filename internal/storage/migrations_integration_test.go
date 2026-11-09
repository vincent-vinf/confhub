package storage_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestMigrationIsIdempotentAndDoesNotCloseApplicationPool(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, true); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAdmin(ctx, ""); err == nil {
		t.Fatal("initialized admin without a password")
	}
	if err := s.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "preserved"}
	saved, err := s.Save(ctx, key, config.Edit{Content: "keep", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{true, false} {
		if err := storage.Migrate("postgres", dsn, initialize); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("migration closed business pool: %v", err)
	}
	if err := s.InitializeAdmin(ctx, "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "admin", "initial-password"); err != nil {
		t.Fatal("admin changed after repeated initialization", err)
	}
	current, _, err := s.Current(ctx, key)
	if err != nil || current.ID != saved.State.ID || current.Versions[1].Content != "keep" {
		t.Fatal(current, err)
	}
}

func TestInitialMigrationCanBeRolledBackAndReapplied(t *testing.T) {
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
	if err := s.InitializeAdmin(ctx, "test-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "roundtrip"}, config.Edit{Content: "value", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://migrations/postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		a, b := m.Close()
		if a != nil || b != nil {
			t.Error(a, b)
		}
	}()
	if err := m.Down(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name<>'schema_migrations'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback left %d tables", count)
	}
	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.InitializeAdmin(ctx, "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "admin", "new-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "fresh"}, config.Edit{Content: "new", Format: "text"}); err != nil {
		t.Fatal(err)
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
	// Model a migration interrupted after setting dirty.
	if _, err := setup.Exec("UPDATE schema_migrations SET dirty=true"); err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{true, false} {
		err := storage.Migrate("postgres", dsn, initialize)
		if err == nil || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("dirty schema accepted: %v", err)
		}
	}
}

func TestNewerSchemaIsRejectedWithoutChangingVersionOrData(t *testing.T) {
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
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "preserved"}
	if _, err := s.Save(ctx, key, config.Edit{Content: "keep", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	setup, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.Close()
	unsupported := storage.SchemaVersion + 1
	if _, err := setup.Exec("UPDATE schema_migrations SET version=$1", unsupported); err != nil {
		t.Fatal(err)
	}
	for _, initialize := range []bool{true, false} {
		if err := storage.Migrate("postgres", dsn, initialize); err == nil {
			t.Fatal("newer schema accepted")
		}
	}
	var version int
	var dirty bool
	if err := setup.QueryRow("SELECT version,dirty FROM schema_migrations").Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != unsupported || dirty {
		t.Fatal(fmt.Sprintf("migration changed schema marker: %d dirty=%v", version, dirty))
	}
	current, _, err := s.Current(ctx, key)
	if err != nil || current.Versions[1].Content != "keep" {
		t.Fatal(current, err)
	}
}
