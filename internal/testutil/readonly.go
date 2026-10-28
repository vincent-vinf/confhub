package testutil

import (
	"database/sql"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ReadOnlyDSN grants SELECT on an owned integration-test schema. Unlike a
// transaction setting, this also catches accidental DDL during initialization.
func ReadOnlyDSN(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := u.Query().Get("search_path")
	if !regexp.MustCompile(`^test_[a-f0-9]+$`).MatchString(schema) {
		t.Fatal("read-only fixture requires an isolated test schema")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	role := "reader_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := uuid.NewString()
	if _, err = db.Exec("CREATE ROLE " + role + " LOGIN PASSWORD '" + password + "'"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		if _, err := db.Exec("DROP OWNED BY " + role); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec("DROP ROLE " + role); err != nil {
			t.Error(err)
		}
	})
	for _, statement := range []string{
		"GRANT USAGE ON SCHEMA " + schema + " TO " + role,
		"GRANT SELECT ON ALL TABLES IN SCHEMA " + schema + " TO " + role,
	} {
		if _, err = db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	u.User = url.UserPassword(role, password)
	return u.String()
}
