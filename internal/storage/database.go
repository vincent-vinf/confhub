// Package storage implements ConfHub's transactional PostgreSQL/MySQL store.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqlsql "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
)

//go:embed migrations/*/*.sql
var migrations embed.FS

const SchemaVersion = 4

type Store struct {
	db      *sql.DB
	dialect string
}

func Open(ctx context.Context, dialect, dsn string) (*Store, error) {
	driver, err := driverName(dialect)
	if err != nil {
		return nil, err
	}
	if dialect == "mysql" {
		cfg, err := mysqlsql.ParseDSN(dsn)
		if err != nil {
			return nil, err
		}
		cfg.ParseTime = true
		cfg.MultiStatements = false
		cfg.Timeout = 5 * time.Second
		dsn = cfg.FormatDSN()
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, dialect: dialect}, nil
}
func driverName(dialect string) (string, error) {
	switch dialect {
	case "postgres":
		return "postgres", nil
	case "mysql":
		return "mysql", nil
	default:
		return "", fmt.Errorf("unsupported database %q", dialect)
	}
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) query(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Migrate uses its own connection pool. initializeOnly refuses to upgrade an
// existing schema; upgrades must be explicitly requested via `confhub migrate`.
func Migrate(dialect, dsn string, initializeOnly bool) (err error) {
	driver, err := driverName(dialect)
	if err != nil {
		return err
	}
	if dialect == "mysql" {
		cfg, e := mysqlsql.ParseDSN(dsn)
		if e != nil {
			return e
		}
		cfg.MultiStatements = true
		cfg.Timeout = 5 * time.Second
		dsn = cfg.FormatDSN()
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	var adapter database.Driver
	if dialect == "postgres" {
		adapter, err = migratepg.WithInstance(db, &migratepg.Config{})
	} else {
		adapter, err = migratemysql.WithInstance(db, &migratemysql.Config{})
	}
	if err != nil {
		return err
	}
	source, err := iofs.New(migrations, "migrations/"+dialect)
	if err != nil {
		adapter.Close()
		return err
	}
	m, err := migrate.NewWithInstance("iofs", source, dialect, adapter)
	if err != nil {
		source.Close()
		adapter.Close()
		return err
	}
	defer func() { a, b := m.Close(); err = errors.Join(err, a, b) }()
	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return err
	}
	if dirty {
		return fmt.Errorf("database schema version %d is dirty; repair schema before migration", version)
	}
	if err == nil && version >= 1 && version <= 3 {
		return fmt.Errorf("legacy schema %d is unsupported by the shared beta model; recreate an empty database instead of migrating existing configuration data", version)
	}

	if initializeOnly && err == nil {
		if version != SchemaVersion {
			return fmt.Errorf("schema version %d; expected %d: run confhub migrate", version, SchemaVersion)
		}
		return nil
	}
	err = m.Up()
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}
