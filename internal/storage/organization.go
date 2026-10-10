package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/vincent-vinf/confhub/internal/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) CreateNamespace(ctx context.Context, name string) error {
	if err := config.ValidateName(name); err != nil {
		return err
	}
	return storageError(s.db.WithContext(ctx).Create(&namespaceRow{Name: name}).Error)
}
func (s *Store) CreateGroup(ctx context.Context, namespace, name string) error {
	if err := config.ValidateName(namespace); err != nil {
		return err
	}
	if err := config.ValidateName(name); err != nil {
		return err
	}
	return storageError(s.db.WithContext(ctx).Create(&groupRow{Namespace: namespace, Name: name}).Error)
}
func (s *Store) Namespaces(ctx context.Context) ([]string, error) {
	names := []string{}
	err := s.db.WithContext(ctx).Model(&namespaceRow{}).Order("name").Pluck("name", &names).Error
	return names, err
}
func (s *Store) Groups(ctx context.Context, namespace string) ([]string, error) {
	names := []string{}
	err := s.db.WithContext(ctx).Model(&groupRow{}).Where(map[string]any{"namespace": namespace}).Order("name").Pluck("name", &names).Error
	return names, err
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
	var result *gorm.DB
	// Foreign keys reject concurrent creation of a child during deletion; the
	// stream lock also coordinates configuration creation/deletion with this check.
	if group == "" {
		result = tx.Where(map[string]any{"name": namespace}).Delete(&namespaceRow{})
	} else {
		result = tx.Where(map[string]any{"namespace": namespace, "name": group}).Delete(&groupRow{})
	}
	if result.Error != nil {
		mapped := storageError(result.Error)
		if errors.Is(mapped, config.ErrInvalid) {
			return config.ErrNotEmpty
		}
		return mapped
	}
	if result.RowsAffected == 0 {
		return config.ErrNotFound
	}
	return tx.Commit().Error
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
	var rows []configRow
	err := s.db.WithContext(ctx).Where(map[string]any{"namespace": namespace, "group_name": group}).
		Where(clause.Gt{Column: "name", Value: after}).Order("name").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]ConfigSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, ConfigSummary{ID: row.ID,
			Key:      config.Key{Namespace: namespace, Group: group, Name: row.Name},
			Revision: row.Revision, GlobalVersion: row.GlobalVersion, LastVersion: row.LastVersion})
	}
	return result, nil
}
