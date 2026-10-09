// Package testutil provisions isolated PostgreSQL schemas for integration tests.
package testutil

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gitlab.bodesitech.com/bodesi/confhub/internal/storage"
)

func Store(t *testing.T) *storage.Store {
	t.Helper()
	dsn := os.Getenv("CONFHUB_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set CONFHUB_TEST_POSTGRES_DSN to run PostgreSQL integration tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	setup, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec("CREATE SCHEMA " + schema); err != nil {
		setup.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { setup.Exec("DROP SCHEMA " + schema + " CASCADE"); setup.Close() })
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	dsn = parsed.String()
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
