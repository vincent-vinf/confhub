package storage

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sourceRow struct {
	ID       int `gorm:"primaryKey;autoIncrement:false"`
	SourceID string
}

func (sourceRow) TableName() string { return "replication_source" }

type mirrorRow struct {
	ID               int `gorm:"primaryKey;autoIncrement:false"`
	SourceID         string
	SourceSequence   int64
	Ready            bool
	LastSuccess      int64
	Owner            string
	Epoch, ExpiresAt int64
	ActiveGeneration string
}

func (mirrorRow) TableName() string { return "replication_mirror" }

func (s *Store) Watermark(ctx context.Context) (replication.Cursor, error) {
	// This identity is durable and shared by all cloud instances.
	var source sourceRow
	if err := s.db.WithContext(ctx).Take(&source, 1).Error; err != nil {
		return replication.Cursor{}, err
	}
	if source.SourceID == "" {
		if err := s.localWritable(); err != nil {
			return replication.Cursor{}, err
		}
		if err := s.db.WithContext(ctx).Model(&sourceRow{}).Where(map[string]any{"id": 1, "source_id": ""}).Update("source_id", uuid.NewString()).Error; err != nil {
			return replication.Cursor{}, err
		}
		if err := s.db.WithContext(ctx).Take(&source, 1).Error; err != nil {
			return replication.Cursor{}, err
		}
	}
	var stream streamRow
	err := s.db.WithContext(ctx).Take(&stream, 1).Error
	return replication.Cursor{Source: source.SourceID, Sequence: stream.Sequence}, err
}

// Export writes length-delimited protobuf aggregates from one repeatable-read view.
// Large incremental windows fall back to a full snapshot, never advance past
// omitted events. Output is staged by the protocol before it is transferred.
func (s *Store) Export(ctx context.Context, after replication.Cursor, out io.Writer) (replication.Manifest, error) {
	current, err := s.Watermark(ctx)
	if err != nil {
		return replication.Manifest{}, err
	}
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return replication.Manifest{}, tx.Error
	}
	defer tx.Rollback()
	var stream streamRow
	if err := tx.Take(&stream, 1).Error; err != nil {
		return replication.Manifest{}, err
	}
	if after.Sequence < 0 || (after.Source != "" && (after.Source != current.Source || after.Sequence > stream.Sequence)) {
		return replication.Manifest{}, replication.ErrSourceChanged
	}
	batch := replication.Manifest{Cursor: replication.Cursor{Source: current.Source, Sequence: stream.Sequence}, After: after.Sequence, Full: after.Source == "" || after.Sequence < stream.PurgedThrough}
	var keys []eventRow
	if !batch.Full {
		err = tx.Model(&eventRow{}).Distinct("namespace", "group_name", "name").Where("sequence > ? AND sequence <= ?", after.Sequence, stream.Sequence).Limit(1001).Find(&keys).Error
		if err != nil {
			return batch, err
		}
		batch.Full = len(keys) > 1000
	}
	enc := replication.NewRecordEncoder(out)
	write := func(k config.Key) error {
		state, err := s.load(ctx, tx, k)
		if err != nil && !errors.Is(err, config.ErrNotFound) {
			return err
		}
		if state != nil {
			state.Sequence = stream.Sequence
		}
		if err := enc.Encode(replication.Record{Key: k, State: state}); err != nil {
			return err
		}
		batch.Count++
		return nil
	}
	if batch.Full {
		last := ""
		for {
			var rows []configRow
			if err := tx.Where("id > ?", last).Order("id").Limit(128).Find(&rows).Error; err != nil {
				return batch, err
			}
			for _, r := range rows {
				if err := write(config.Key{Namespace: r.Namespace, Group: r.GroupName, Name: r.Name}); err != nil {
					return batch, err
				}
				last = r.ID
			}
			if len(rows) < 128 {
				break
			}
		}
	} else {
		for _, r := range keys {
			if err := write(config.Key{Namespace: r.Namespace, Group: r.GroupName, Name: r.Name}); err != nil {
				return batch, err
			}
		}
	}
	return batch, tx.Commit().Error
}

func (s *Store) MirrorStatus(ctx context.Context) (replication.Status, error) {
	var row mirrorRow
	err := s.db.WithContext(ctx).Take(&row, 1).Error
	status := replication.Status{Cursor: replication.Cursor{Source: row.SourceID, Sequence: row.SourceSequence}, Ready: row.Ready, Owner: row.Owner}
	if row.LastSuccess > 0 {
		status.LastSuccess = time.UnixMicro(row.LastSuccess).UTC()
	}
	if row.ExpiresAt > 0 {
		status.LeaseExpires = time.UnixMicro(row.ExpiresAt).UTC()
	}
	return status, err
}

func (s *Store) AcquireMirrorLease(ctx context.Context, owner string, duration time.Duration) (replication.Lease, bool, error) {
	if err := s.localWritable(); err != nil {
		return replication.Lease{}, false, err
	}
	if owner == "" || len(owner) > 36 || duration < time.Millisecond {
		return replication.Lease{}, false, config.ErrInvalid
	}
	var result replication.Lease
	acquired := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row mirrorRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row, 1).Error; err != nil {
			return err
		}
		now, err := s.databaseTime(tx)
		if err != nil {
			return err
		}
		if row.Owner != owner && row.ExpiresAt > now {
			return nil
		}
		if row.Owner != owner || row.ExpiresAt <= now {
			row.Epoch++
		}
		if err := tx.Model(&row).Updates(map[string]any{"owner": owner, "epoch": row.Epoch, "expires_at": now + duration.Microseconds()}).Error; err != nil {
			return err
		}
		result = replication.Lease{Owner: owner, Epoch: row.Epoch}
		acquired = true
		return nil
	})
	return result, acquired, err
}
func (s *Store) ReleaseMirrorLease(ctx context.Context, lease replication.Lease) error {
	if err := s.localWritable(); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&mirrorRow{}).Where(map[string]any{"id": 1, "owner": lease.Owner, "epoch": lease.Epoch}).Updates(map[string]any{"owner": "", "expires_at": 0}).Error
}

func validateRecord(r replication.Record, full bool) error {
	if err := r.Key.Validate(); err != nil {
		return err
	}
	if r.State == nil {
		if full {
			return config.ErrInvalid
		}
		return nil
	}
	st := r.State
	if _, err := uuid.Parse(st.ID); err != nil {
		return config.ErrInvalid
	}
	if st.Key != r.Key || st.Revision < 1 || st.GlobalVersion < 1 || st.LastVersion < st.GlobalVersion || len(st.Versions) != 1 {
		return config.ErrInvalid
	}
	v, ok := st.Versions[st.GlobalVersion]
	if !ok || v.Number != st.GlobalVersion || len(v.Description) > 4096 {
		return config.ErrInvalid
	}
	if err := config.ValidateContent(v.Format, v.Content); err != nil {
		return err
	}
	if err := config.ValidateRules(st.Rules); err != nil {
		return err
	}
	if (len(st.Rules) > 0) != (st.Beta != nil) {
		return config.ErrInvalid
	}
	if st.Beta != nil {
		if len(st.Beta.Description) > 4096 {
			return config.ErrInvalid
		}
		return config.ValidateContent(st.Beta.Format, st.Beta.Content)
	}
	return nil
}

// MirrorSource gates the existing local Hub until a complete mirror is present.
// Cloud connectivity has no influence on this readiness decision.
type MirrorSource struct{ Store *Store }

func (s MirrorSource) Changes(ctx context.Context, after int64, limit int) (config.Changes, error) {
	status, err := s.Store.MirrorStatus(ctx)
	if err != nil {
		return config.Changes{}, err
	}
	if !status.Ready {
		return config.Changes{}, replication.ErrUninitialized
	}
	return s.Store.Changes(ctx, after, limit)
}
func (s MirrorSource) Current(ctx context.Context, key config.Key) (*config.State, int64, error) {
	return s.Store.MirrorCurrent(ctx, key)
}

var _ replication.Source = (*Store)(nil)
var _ replication.Mirror = (*Store)(nil)
