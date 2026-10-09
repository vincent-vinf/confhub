package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func (s *Store) CreateNamespace(ctx context.Context, name string) error {
	if err := config.ValidateName(name); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.query("INSERT INTO namespaces(name) VALUES (?)"), name)
	return storageError(err)
}
func (s *Store) CreateGroup(ctx context.Context, namespace, name string) error {
	if err := config.ValidateName(namespace); err != nil {
		return err
	}
	if err := config.ValidateName(name); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.query("INSERT INTO config_groups(namespace,name) VALUES (?,?)"), namespace, name)
	return storageError(err)
}
func (s *Store) Namespaces(ctx context.Context) ([]string, error) {
	return s.names(ctx, "SELECT name FROM namespaces ORDER BY name")
}
func (s *Store) Groups(ctx context.Context, namespace string) ([]string, error) {
	return s.names(ctx, "SELECT name FROM config_groups WHERE namespace=? ORDER BY name", namespace)
}
func (s *Store) names(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.query(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (s *Store) DeleteNamespace(ctx context.Context, namespace string) error {
	return s.deleteOrganization(ctx, namespace, "")
}
func (s *Store) DeleteGroup(ctx context.Context, namespace, group string) error {
	return s.deleteOrganization(ctx, namespace, group)
}
func (s *Store) deleteOrganization(ctx context.Context, namespace, group string) error {
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	// Foreign keys reject concurrent creation of a child during deletion; the
	// stream lock also coordinates configuration creation/deletion with this check.
	if group == "" {
		result, err = tx.ExecContext(ctx, s.query("DELETE FROM namespaces WHERE name=?"), namespace)
	} else {
		result, err = tx.ExecContext(ctx, s.query("DELETE FROM config_groups WHERE namespace=? AND name=?"), namespace, group)
	}
	if err != nil {
		mapped := storageError(err)
		if errors.Is(mapped, config.ErrInvalid) {
			return config.ErrNotEmpty
		}
		return mapped
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return config.ErrNotFound
	}
	return tx.Commit()
}

type ConfigSummary struct {
	ID            string     `json:"id"`
	Key           config.Key `json:"key"`
	Revision      int64      `json:"revision"`
	GlobalVersion int64      `json:"global_version"`
	LastVersion   int64      `json:"last_version"`
}

func (s *Store) List(ctx context.Context, namespace, group, after string, limit int) ([]ConfigSummary, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: limit must be 1–200", config.ErrInvalid)
	}
	rows, err := s.db.QueryContext(ctx, s.query("SELECT id,name,revision,global_version,last_version FROM configs WHERE namespace=? AND group_name=? AND name>? ORDER BY name LIMIT ?"), namespace, group, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ConfigSummary{}
	for rows.Next() {
		v := ConfigSummary{Key: config.Key{Namespace: namespace, Group: group}}
		if err = rows.Scan(&v.ID, &v.Key.Name, &v.Revision, &v.GlobalVersion, &v.LastVersion); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
