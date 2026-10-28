package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Keep the active-generation pointer and its rows in the same repeatable-read
// view, so activation/cleanup cannot expose partial or retired catalogs.
func (s *Store) mirrorCatalog(ctx context.Context, read func(*gorm.DB, bool) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var mirror mirrorRow
		if err := tx.Take(&mirror, 1).Error; err != nil {
			return err
		}
		if !mirror.Ready {
			return replication.ErrUninitialized
		}
		legacy := mirror.ActiveGeneration == ""
		query := tx.Model(&configRow{})
		if !legacy {
			query = tx.Model(&mirrorConfigRow{}).Where("generation = ?", mirror.ActiveGeneration)
		}
		return read(query, legacy)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}

func (s *Store) MirrorNamespaces(ctx context.Context) ([]string, error) {
	names := []string{}
	err := s.mirrorCatalog(ctx, func(q *gorm.DB, _ bool) error {
		return q.Distinct("namespace").Order("namespace").Pluck("namespace", &names).Error
	})
	return names, err
}

func (s *Store) MirrorGroups(ctx context.Context, namespace string) ([]string, error) {
	names := []string{}
	err := s.mirrorCatalog(ctx, func(q *gorm.DB, _ bool) error {
		return q.Where("namespace = ?", namespace).Distinct("group_name").Order("group_name").Pluck("group_name", &names).Error
	})
	return names, err
}

func (s *Store) MirrorList(ctx context.Context, namespace, group, after string, limit int) ([]ConfigSummary, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: limit must be 1–200", config.ErrInvalid)
	}
	result := []ConfigSummary{}
	err := s.mirrorCatalog(ctx, func(q *gorm.DB, legacy bool) error {
		q = q.Where("namespace = ? AND group_name = ?", namespace, group).Where(clause.Gt{Column: "name", Value: after}).Order("name").Limit(limit)
		if legacy {
			var rows []configRow
			if err := q.Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				result = append(result, ConfigSummary{ID: row.ID, Key: config.Key{Namespace: namespace, Group: group, Name: row.Name}, Revision: row.Revision, GlobalVersion: row.GlobalVersion, LastVersion: row.LastVersion})
			}
			return nil
		}
		var rows []mirrorConfigRow
		if err := q.Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			state, err := row.state()
			if err != nil {
				return err
			}
			result = append(result, ConfigSummary{ID: state.ID, Key: state.Key, Revision: state.Revision, GlobalVersion: state.GlobalVersion, LastVersion: state.LastVersion})
		}
		return nil
	})
	return result, err
}
