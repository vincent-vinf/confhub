package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/configarchive"
	"gorm.io/gorm"
)

// ExportArchive reads one consistent catalog snapshot, including across pages.
// mirror selects the edge's active generation rather than its management tables.
func (s *Store) ExportArchive(ctx context.Context, namespace, group string, names []string, search string, mirror bool) ([]configarchive.Item, error) {
	if err := (config.Key{Namespace: namespace, Group: group, Name: "export"}).Validate(); err != nil {
		return nil, err
	}
	if len(names) > configarchive.MaxItems {
		return nil, fmt.Errorf("%w: select at most 1000 configurations", config.ErrInvalid)
	}
	selected := map[string]bool{}
	for _, name := range names {
		if err := config.ValidateName(name); err != nil {
			return nil, err
		}
		if selected[name] {
			return nil, fmt.Errorf("%w: duplicate selected name", config.ErrInvalid)
		}
		selected[name] = true
	}
	items := []configarchive.Item{}
	total := 0
	appendVersion := func(name string, v config.Version) error {
		if len(items) >= configarchive.MaxItems {
			return fmt.Errorf("%w: export exceeds 1000 configurations", config.ErrInvalid)
		}
		total += len(v.Content)
		if total > configarchive.MaxBytes {
			return fmt.Errorf("%w: export exceeds 64 MiB", config.ErrInvalid)
		}
		items = append(items, configarchive.Item{Group: group, Name: name, Content: v.Content, Format: v.Format, Description: v.Description})
		return nil
	}
	read := func(q *gorm.DB, legacy bool) error {
		q = q.Where("namespace = ? AND group_name = ?", namespace, group).Order("name")
		if len(names) > 0 {
			q = q.Where("name IN ?", names)
		}
		matches := func(name string) bool { return strings.Contains(strings.ToLower(name), strings.ToLower(search)) }
		if legacy {
			// Scan only names/identities first, so an over-limit request does not load bodies.
			var rows []configRow
			if err := q.Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				if !matches(row.Name) {
					continue
				}
				if len(items) >= configarchive.MaxItems {
					return fmt.Errorf("%w: export exceeds 1000 configurations", config.ErrInvalid)
				}
				v, err := loadVersion(q.Session(&gorm.Session{NewDB: true}), row.ID, row.GlobalVersion)
				if err != nil {
					return err
				}
				if err = appendVersion(row.Name, v); err != nil {
					return err
				}
			}
		} else {
			// Keyset pages bound decoded mirror bodies while staying in one generation view.
			after := ""
			for {
				var rows []mirrorConfigRow
				if err := q.Session(&gorm.Session{}).Where("name > ?", after).Limit(1).Find(&rows).Error; err != nil {
					return err
				}
				if len(rows) == 0 {
					break
				}
				row := rows[0]
				after = row.Name
				if !matches(row.Name) {
					continue
				}
				state, err := row.state()
				if err != nil {
					return err
				}
				if err = appendVersion(row.Name, state.Versions[state.GlobalVersion]); err != nil {
					return err
				}
			}
		}
		if len(names) > 0 && len(items) != len(names) {
			return config.ErrNotFound
		}
		if len(items) == 0 {
			return fmt.Errorf("%w: no configurations to export", config.ErrInvalid)
		}
		return nil
	}
	var err error
	if mirror {
		err = s.mirrorCatalog(ctx, read)
	} else {
		err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return read(tx.Model(&configRow{}), true) }, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	return items, err
}
