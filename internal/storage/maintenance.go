package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Cleanup performs one bounded batch while holding the same mutation lock as
// publication. A database-clock lease chooses a temporary maintenance worker.
func (s *Store) Cleanup(ctx context.Context, owner string, history int, retention time.Duration, batch int) (bool, error) {
	if owner == "" || len(owner) > 36 || history < 1 || retention <= 0 || batch < 1 || batch > 1000 {
		return false, fmt.Errorf("invalid cleanup parameters")
	}
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now, err := s.databaseTime(tx)
	if err != nil {
		return false, err
	}
	var lease leaseRow
	if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&lease, 1).Error; err != nil {
		return false, err
	}
	if lease.Owner != owner && lease.ExpiresAt > now {
		return false, nil
	}
	if err = tx.Model(&leaseRow{}).Where(map[string]any{"id": 1}).Updates(map[string]any{
		"owner": owner, "expires_at": now + (10 * time.Second).Microseconds(),
	}).Error; err != nil {
		return false, err
	}
	var candidates []versionRow
	err = tx.Table("config_versions AS v").Select("v.config_id", "v.number").Clauses(clause.From{
		Tables: []clause.Table{{Name: "config_versions", Alias: "v"}},
		Joins: []clause.Join{{Type: clause.InnerJoin, Table: clause.Table{Name: "configs", Alias: "c"},
			ON: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: column("c", "id"), Value: column("v", "config_id")}}}}},
	}).Where(clause.Lte{Column: column("v", "number"), Value: gorm.Expr("? - ?", column("c", "last_version"), history)}).
		Where(clause.Neq{Column: column("v", "number"), Value: column("c", "global_version")}).
		Order(clause.OrderByColumn{Column: column("v", "config_id")}).
		Order(clause.OrderByColumn{Column: column("v", "number")}).Limit(batch).Find(&candidates).Error
	if err != nil {
		return false, err
	}
	for _, row := range candidates {
		if err = tx.Where(map[string]any{"config_id": row.ConfigID, "number": row.Number}).Delete(&versionRow{}).Error; err != nil {
			return false, err
		}
	}
	var events []eventRow
	if err = tx.Select("sequence", "created_at").Order("sequence").Limit(batch).Find(&events).Error; err != nil {
		return false, err
	}
	through := int64(0)
	cutoff := now - retention.Microseconds()
	for _, event := range events {
		if event.CreatedAt >= cutoff {
			break
		}
		through = event.Sequence
	}
	if through > 0 {
		if err = tx.Where(clause.Lte{Column: "sequence", Value: through}).Delete(&eventRow{}).Error; err != nil {
			return false, err
		}
		if err = tx.Model(&streamRow{}).Where(map[string]any{"id": 1}).Update("purged_through", through).Error; err != nil {
			return false, err
		}
	}
	return true, tx.Commit().Error
}
