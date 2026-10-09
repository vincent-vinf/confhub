package storage

import (
	"context"
	"fmt"
	"time"
)

// Cleanup performs one bounded batch while holding the same mutation lock as
// rule binding. A database-clock lease chooses a temporary maintenance worker.
func (s *Store) Cleanup(ctx context.Context, owner string, history int, retention time.Duration, batch int) (bool, error) {
	if owner == "" || len(owner) > 36 || history < 1 || retention <= 0 || batch < 1 || batch > 1000 {
		return false, fmt.Errorf("invalid cleanup parameters")
	}
	tx, _, err := s.beginMutation(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var now int64
	clockQuery := "SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp())*1000000 AS BIGINT)"
	if s.dialect == "mysql" {
		clockQuery = "SELECT CAST(UNIX_TIMESTAMP(NOW(6))*1000000 AS SIGNED)"
	}
	if err = tx.QueryRowContext(ctx, clockQuery).Scan(&now); err != nil {
		return false, err
	}
	var current string
	var expires int64
	if err = tx.QueryRowContext(ctx, "SELECT owner,expires_at FROM maintenance_lease WHERE id=1 FOR UPDATE").Scan(&current, &expires); err != nil {
		return false, err
	}
	if current != owner && expires > now {
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, s.query("UPDATE maintenance_lease SET owner=?,expires_at=? WHERE id=1"), owner, now+(10*time.Second).Microseconds()); err != nil {
		return false, err
	}
	rows, err := tx.QueryContext(ctx, s.query("SELECT v.config_id,v.number FROM config_versions v JOIN configs c ON c.id=v.config_id WHERE v.number<=c.last_version-? AND v.number<>c.global_version AND NOT EXISTS (SELECT 1 FROM gray_rules r WHERE r.config_id=v.config_id AND r.target_version=v.number) ORDER BY v.config_id,v.number LIMIT ?"), history, batch)
	if err != nil {
		return false, err
	}
	type candidate struct {
		id     string
		number int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.number); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return false, err
	}
	for _, c := range candidates {
		if _, err = tx.ExecContext(ctx, s.query("DELETE FROM config_versions WHERE config_id=? AND number=?"), c.id, c.number); err != nil {
			return false, err
		}
	}
	rows, err = tx.QueryContext(ctx, s.query("SELECT sequence,created_at FROM change_events ORDER BY sequence LIMIT ?"), batch)
	if err != nil {
		return false, err
	}
	through := int64(0)
	cutoff := now - retention.Microseconds()
	for rows.Next() {
		var seq, created int64
		if err = rows.Scan(&seq, &created); err != nil {
			break
		}
		if created >= cutoff {
			break
		}
		through = seq
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return false, err
	}
	if through > 0 {
		if _, err = tx.ExecContext(ctx, s.query("DELETE FROM change_events WHERE sequence<=?"), through); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, s.query("UPDATE change_stream SET purged_through=? WHERE id=1"), through); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}
