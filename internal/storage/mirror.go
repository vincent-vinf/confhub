package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type mirrorGenerationRow struct {
	ID      string `gorm:"primaryKey"`
	Owner   string
	Epoch   int64
	Staging bool
}

func (mirrorGenerationRow) TableName() string { return "replication_generations" }

type mirrorConfigRow struct {
	Generation string `gorm:"primaryKey"`
	Namespace  string `gorm:"primaryKey"`
	GroupName  string `gorm:"primaryKey"`
	Name       string `gorm:"primaryKey"`
	ConfigID   string
	StateProto []byte  `gorm:"column:state_proto"`
	LegacyJSON *string `gorm:"column:state_json;->"`
}

func (mirrorConfigRow) TableName() string { return "replication_configs" }

func (row mirrorConfigRow) state() (*config.State, error) {
	if row.StateProto != nil {
		return replication.UnmarshalState(row.StateProto)
	}
	if row.LegacyJSON != nil {
		state := &config.State{}
		err := json.Unmarshal([]byte(*row.LegacyJSON), state)
		return state, err
	}
	return nil, errors.New("missing mirror state")
}

// MirrorCurrent reads the active generation and local cursor from one view.
// Schema-7 mirrors remain readable offline until their next successful import.
func (s *Store) MirrorCurrent(ctx context.Context, key config.Key) (*config.State, int64, error) {
	if err := key.Validate(); err != nil {
		return nil, 0, err
	}
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	defer tx.Rollback()
	var mirror mirrorRow
	if err := tx.Take(&mirror, 1).Error; err != nil {
		return nil, 0, err
	}
	if !mirror.Ready {
		return nil, 0, replication.ErrUninitialized
	}
	var stream streamRow
	if err := tx.Take(&stream, 1).Error; err != nil {
		return nil, 0, err
	}
	var state *config.State
	var readErr error
	if mirror.ActiveGeneration == "" {
		state, readErr = s.load(ctx, tx, key)
	} else {
		var row mirrorConfigRow
		readErr = storageError(configKey(tx, key).Where("generation = ?", mirror.ActiveGeneration).Take(&row).Error)
		if readErr == nil {
			state, readErr = row.state()
		}
	}
	if readErr != nil && !errors.Is(readErr, config.ErrNotFound) {
		return nil, 0, readErr
	}
	if err := tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	if state != nil {
		state.Sequence = stream.Sequence
	}
	return state, stream.Sequence, readErr
}

func (s *Store) checkedMirror(db *gorm.DB, lease replication.Lease, lock bool) (mirrorRow, error) {
	var row mirrorRow
	if lock {
		db = db.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := db.Take(&row, 1).Error; err != nil {
		return row, err
	}
	now, err := s.databaseTime(db)
	if err != nil {
		return row, err
	}
	if row.Owner != lease.Owner || row.Epoch != lease.Epoch || row.ExpiresAt <= now {
		return row, replication.ErrLeaseLost
	}
	return row, nil
}
func checkMirrorBatch(row mirrorRow, batch replication.Manifest) error {
	if row.Ready && (row.SourceID != batch.Source || batch.Sequence < row.SourceSequence) {
		return replication.ErrSourceChanged
	}
	if !batch.Full && (!row.Ready || batch.After != row.SourceSequence) {
		return config.ErrConflict
	}
	return nil
}
func mirrorRecord(generation string, r replication.Record) (mirrorConfigRow, error) {
	raw, err := replication.MarshalState(r.State)
	return mirrorConfigRow{Generation: generation, Namespace: r.Key.Namespace, GroupName: r.Key.Group, Name: r.Key.Name, ConfigID: r.State.ID, StateProto: raw}, err
}

// ApplyMirror validates before touching the database. Full snapshots build a
// private generation in bounded transactions, then atomically switch one pointer.
// Increments remain bounded by the cloud's 1,000-key export window.
func (s *Store) ApplyMirror(ctx context.Context, lease replication.Lease, batch replication.Manifest, input io.Reader) error {
	if err := s.localWritable(); err != nil {
		return err
	}
	if batch.Source == "" || batch.Sequence < 0 || batch.Count < 0 || batch.After < 0 {
		return config.ErrInvalid
	}
	file, err := replication.StageRecords(ctx, input, batch.Count, func(r replication.Record) error { return validateRecord(r, batch.Full) })
	if err != nil {
		return err
	}
	defer file.Close()
	base, err := s.checkedMirror(s.db.WithContext(ctx), lease, false)
	if err != nil {
		return err
	}
	if base.Ready && base.SourceID == batch.Source && base.SourceSequence == batch.Sequence {
		return nil
	}
	if err := checkMirrorBatch(base, batch); err != nil {
		return err
	}
	if batch.Full || base.ActiveGeneration == "" {
		return s.replaceMirror(ctx, lease, batch, file, base)
	}
	return s.incrementMirror(ctx, lease, batch, file)
}

func (s *Store) replaceMirror(ctx context.Context, lease replication.Lease, batch replication.Manifest, file *replication.StagedFile, base mirrorRow) error {
	generation := uuid.NewString()
	if err := s.db.WithContext(ctx).Create(&mirrorGenerationRow{ID: generation, Owner: lease.Owner, Epoch: lease.Epoch, Staging: true}).Error; err != nil {
		return err
	}
	activated := false
	defer func() {
		if !activated {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.db.WithContext(cleanupCtx).Model(&mirrorGenerationRow{}).Where("id = ?", generation).Update("staging", false).Error
		}
	}()
	rows := make([]mirrorConfigRow, 0, 128)
	size := 0
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		if _, err := s.checkedMirror(s.db.WithContext(ctx), lease, false); err != nil {
			return err
		}
		// Private rows do not lock the lease or visible stream while being written.
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return tx.Create(&rows).Error }); err != nil {
			return err
		}
		rows = rows[:0]
		size = 0
		return nil
	}
	appendRecord := func(r replication.Record) error {
		row, err := mirrorRecord(generation, r)
		if err != nil {
			return err
		}
		if len(rows) > 0 && (len(rows) >= 128 || size+len(row.StateProto) > 4<<20) {
			if err := flush(); err != nil {
				return err
			}
		}
		rows = append(rows, row)
		size += len(row.StateProto)
		return nil
	}
	// Upgrade an existing schema-7 mirror without requiring cloud connectivity at
	// startup. The first increment copies its immutable legacy baseline privately.
	if !batch.Full {
		if err := s.copyLegacyMirror(ctx, appendRecord); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
		dec := replication.NewRecordDecoder(file)
		for i := 0; i < batch.Count; i++ {
			var r replication.Record
			if err := dec.Decode(&r); err != nil {
				return err
			}
			if err := configKey(s.db.WithContext(ctx), r.Key).Where("generation = ?", generation).Delete(&mirrorConfigRow{}).Error; err != nil {
				return err
			}
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	dec := replication.NewRecordDecoder(file)
	for i := 0; i < batch.Count; i++ {
		var r replication.Record
		if err := dec.Decode(&r); err != nil {
			return err
		}
		if r.State != nil {
			if err := appendRecord(r); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	tx, seq, err := s.beginStreamMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := s.checkedMirror(tx, lease, true)
	if err != nil {
		return err
	}
	if err := checkMirrorBatch(current, batch); err != nil {
		return err
	}
	if current.ActiveGeneration != base.ActiveGeneration || current.SourceSequence != base.SourceSequence {
		return config.ErrConflict
	}
	now, err := s.databaseTime(tx)
	if err != nil {
		return err
	}
	if err := tx.Model(&mirrorGenerationRow{}).Where("id = ?", generation).Update("staging", false).Error; err != nil {
		return err
	}
	// A durable gap invalidates every instance's cache and refreshes subscriptions,
	// including keys removed by this full replacement, without N event inserts.
	if err := tx.Model(&streamRow{}).Where("id = ?", 1).Updates(map[string]any{"sequence": seq + 1, "purged_through": seq + 1}).Error; err != nil {
		return err
	}
	if err := tx.Model(&mirrorRow{}).Where("id = ?", 1).Updates(map[string]any{"active_generation": generation, "source_id": batch.Source, "source_sequence": batch.Sequence, "ready": true, "last_success": now}).Error; err != nil {
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	activated = true
	return nil
}

func (s *Store) copyLegacyMirror(ctx context.Context, appendRecord func(replication.Record) error) error {
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return tx.Error
	}
	defer tx.Rollback()
	last := ""
	for {
		var rows []configRow
		if err := tx.Where("id > ?", last).Order("id").Limit(128).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			key := config.Key{Namespace: row.Namespace, Group: row.GroupName, Name: row.Name}
			state, err := s.load(ctx, tx, key)
			if err != nil {
				return err
			}
			if err := appendRecord(replication.Record{Key: key, State: state}); err != nil {
				return err
			}
			last = row.ID
		}
		if len(rows) < 128 {
			break
		}
	}
	return tx.Commit().Error
}

func (s *Store) incrementMirror(ctx context.Context, lease replication.Lease, batch replication.Manifest, file *replication.StagedFile) error {
	tx, seq, err := s.beginStreamMutation(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	mirror, err := s.checkedMirror(tx, lease, true)
	if err != nil {
		return err
	}
	if mirror.Ready && mirror.SourceID == batch.Source && mirror.SourceSequence == batch.Sequence {
		return tx.Commit().Error
	}
	if err := checkMirrorBatch(mirror, batch); err != nil {
		return err
	}
	dec := replication.NewRecordDecoder(file)
	for i := 0; i < batch.Count; i++ {
		var r replication.Record
		if err := dec.Decode(&r); err != nil {
			return err
		}
		var row mirrorConfigRow
		err := configKey(tx, r.Key).Where("generation = ?", mirror.ActiveGeneration).Select("generation", "namespace", "group_name", "name", "config_id").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		seq, err = s.appendEvent(ctx, tx, &config.State{ID: row.ConfigID, Key: r.Key}, seq, true)
		if err != nil {
			return err
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	dec = replication.NewRecordDecoder(file)
	for i := 0; i < batch.Count; i++ {
		var r replication.Record
		if err := dec.Decode(&r); err != nil {
			return err
		}
		if r.State == nil {
			continue
		}
		row, err := mirrorRecord(mirror.ActiveGeneration, r)
		if err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		seq, err = s.appendEvent(ctx, tx, r.State, seq, false)
		if err != nil {
			return err
		}
	}
	now, err := s.databaseTime(tx)
	if err != nil {
		return err
	}
	if err := tx.Model(&mirrorRow{}).Where("id = ?", 1).Updates(map[string]any{"source_id": batch.Source, "source_sequence": batch.Sequence, "ready": true, "last_success": now}).Error; err != nil {
		return err
	}
	return tx.Commit().Error
}

// cleanupMirrors reclaims at most batch retired rows per maintenance transaction.
// Holding the mirror row prevents activation racing with generation collection.
func (s *Store) cleanupMirrors(tx *gorm.DB, now int64, batch int) error {
	var mirror mirrorRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&mirror, 1).Error; err != nil {
		return err
	}
	var generation mirrorGenerationRow
	query := tx.Where("id <> ?", mirror.ActiveGeneration)
	if mirror.ExpiresAt > now {
		query = query.Where("staging = ? OR owner <> ? OR epoch <> ?", false, mirror.Owner, mirror.Epoch)
	}
	err := query.Order("id").Take(&generation).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		var rows []mirrorConfigRow
		if err := tx.Where("generation = ?", generation.ID).Select("generation", "namespace", "group_name", "name").Order("namespace,group_name,name").Limit(batch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			return tx.Delete(&rows).Error
		}
		return tx.Delete(&generation).Error
	}
	if mirror.ActiveGeneration != "" {
		var rows []configRow
		if err := tx.Select("id").Order("id").Limit(batch).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			return tx.Delete(&rows).Error
		}
	}
	return nil
}
