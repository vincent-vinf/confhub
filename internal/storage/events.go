package storage

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

// Changes provides an independent broadcast cursor for each caller, together
// with a durable purge watermark from the same database snapshot.
func (s *Store) Changes(ctx context.Context, after int64, limit int) (config.Changes, error) {
	if limit < 1 || limit > 1000 {
		return config.Changes{}, fmt.Errorf("%w: event limit", config.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return config.Changes{}, err
	}
	defer tx.Rollback()
	var result config.Changes
	err = tx.QueryRowContext(ctx, "SELECT sequence,purged_through FROM change_stream WHERE id=1").Scan(&result.Sequence, &result.PurgedThrough)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, s.query("SELECT sequence,config_id,namespace,group_name,name,deleted FROM change_events WHERE sequence>? ORDER BY sequence LIMIT ?"), after, limit)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var e config.Event
		if err = rows.Scan(&e.Sequence, &e.ID, &e.Key.Namespace, &e.Key.Group, &e.Key.Name, &e.Deleted); err != nil {
			break
		}
		result.Events = append(result.Events, e)
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
