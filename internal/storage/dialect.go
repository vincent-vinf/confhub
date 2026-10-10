package storage

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Database time preserves lease semantics across replicas with different clocks.
// PostgreSQL CURRENT_TIMESTAMP is transaction time, not the advancing clock.
func (s *Store) databaseTime(db *gorm.DB) (int64, error) {
	query := "SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp())*1000000 AS BIGINT)"
	if s.dialect == "mysql" {
		query = "SELECT CAST(UNIX_TIMESTAMP(NOW(6))*1000000 AS SIGNED)"
	}
	var now int64
	err := db.Raw(query).Row().Scan(&now)
	return now, err
}

// Tags are binary columns so case, NUL and whitespace retain literal meaning.
// GORM handles binding/quoting; the bytea LIKE operator requires explicit casts.
func (s *Store) tagPrefix(column clause.Column, prefix string) clause.Expression {
	pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix) + "%"
	if s.dialect == "postgres" {
		return gorm.Expr("? LIKE ?::bytea ESCAPE '!'::bytea", column, []byte(pattern))
	}
	return gorm.Expr("? LIKE ? ESCAPE '!'", column, []byte(pattern))
}

func column(table, name string) clause.Column {
	return clause.Column{Table: table, Name: name}
}
