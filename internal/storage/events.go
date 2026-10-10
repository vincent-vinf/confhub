package storage

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/vincent-vinf/confhub/internal/config"
	"gorm.io/gorm/clause"
)

// Changes provides an independent broadcast cursor for each caller, together
// with a durable purge watermark from the same database snapshot.
func (s *Store) Changes(ctx context.Context, after int64, limit int) (config.Changes, error) {
	if limit < 1 || limit > 1000 {
		return config.Changes{}, fmt.Errorf("%w: event limit", config.ErrInvalid)
	}
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return config.Changes{}, tx.Error
	}
	defer tx.Rollback()
	var stream streamRow
	if err := tx.Take(&stream, 1).Error; err != nil {
		return config.Changes{}, err
	}
	result := config.Changes{Sequence: stream.Sequence, PurgedThrough: stream.PurgedThrough}
	var rows []eventRow
	if err := tx.Select("sequence", "config_id", "namespace", "group_name", "name", "deleted").
		Where(clause.Gt{Column: "sequence", Value: after}).Order("sequence").Limit(limit).Find(&rows).Error; err != nil {
		return result, err
	}
	for _, row := range rows {
		result.Events = append(result.Events, config.Event{Sequence: row.Sequence, ID: row.ConfigID,
			Key: config.Key{Namespace: row.Namespace, Group: row.GroupName, Name: row.Name}, Deleted: row.Deleted})
	}
	return result, tx.Commit().Error
}
