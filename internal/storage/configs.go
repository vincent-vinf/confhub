package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func storageError(err error) error {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return config.ErrNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return config.ErrConflict
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		return fmt.Errorf("%w: referenced organization or version missing", config.ErrInvalid)
	default:
		return err
	}
}

func (s *Store) load(ctx context.Context, db *gorm.DB, k config.Key) (*config.State, error) {
	db = db.WithContext(ctx)
	var row configRow
	if err := configKey(db, k).Take(&row).Error; err != nil {
		return nil, storageError(err)
	}
	state := &config.State{ID: row.ID, Key: k, Revision: row.Revision,
		LastVersion: row.LastVersion, GlobalVersion: row.GlobalVersion,
		Rules: []config.Rule{}, Versions: map[int64]config.Version{}}
	var beta betaRow
	err := db.Where(map[string]any{"config_id": state.ID}).Take(&beta).Error
	if err == nil {
		state.Beta = &config.Beta{}
		if err = json.Unmarshal([]byte(beta.BetaJSON), state.Beta); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rules []ruleRow
	if err = db.Where(map[string]any{"config_id": state.ID}).Order("position").Find(&rules).Error; err != nil {
		return nil, err
	}
	for _, row := range rules {
		var rule config.Rule
		if err = json.Unmarshal([]byte(row.RuleJSON), &rule); err != nil {
			return nil, err
		}
		state.Rules = append(state.Rules, rule)
	}
	var versions []versionRow
	if err = db.Where(map[string]any{"config_id": state.ID, "number": state.GlobalVersion}).Find(&versions).Error; err != nil {
		return nil, err
	}
	for _, row := range versions {
		state.Versions[row.Number] = row.version()
	}
	return state, nil
}

func (s *Store) Snapshot(ctx context.Context, k config.Key) (*config.State, error) {
	state, _, err := s.Current(ctx, k)
	return state, err
}

// Current returns a state (including absence) and commit watermark from one
// repeatable-read snapshot, so late reads cannot overwrite newer notifications.
func (s *Store) Current(ctx context.Context, k config.Key) (*config.State, int64, error) {
	if err := k.Validate(); err != nil {
		return nil, 0, err
	}
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	var err error
	defer tx.Rollback()
	var stream streamRow
	if err = tx.Take(&stream, 1).Error; err != nil {
		return nil, 0, err
	}
	sequence := stream.Sequence
	state, readErr := s.load(ctx, tx, k)
	if readErr != nil && !errors.Is(readErr, config.ErrNotFound) {
		return nil, 0, readErr
	}
	if err = tx.Commit().Error; err != nil {
		return nil, 0, err
	}
	if state != nil {
		state.Sequence = sequence
	}
	return state, sequence, readErr
}
func (s *Store) Version(ctx context.Context, k config.Key, number int64) (config.Version, error) {
	var row versionRow
	err := s.db.WithContext(ctx).Table("config_versions AS v").Select("v.*").Clauses(clause.From{
		Tables: []clause.Table{{Name: "config_versions", Alias: "v"}},
		Joins: []clause.Join{{Type: clause.InnerJoin, Table: clause.Table{Name: "configs", Alias: "c"},
			ON: clause.Where{Exprs: []clause.Expression{clause.Eq{Column: column("c", "id"), Value: column("v", "config_id")}}}}},
	}).Where(map[string]any{"c.namespace": k.Namespace, "c.group_name": k.Group, "c.name": k.Name, "v.number": number}).Take(&row).Error
	return row.version(), storageError(err)
}

func (s *Store) beginMutation(ctx context.Context) (*gorm.DB, int64, error) {
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if tx.Error != nil {
		return nil, 0, tx.Error
	}
	var stream streamRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&stream, 1).Error; err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	return tx, stream.Sequence, nil
}
func checkEdit(state *config.State, id string, revision int64) error {
	if id != state.ID || revision != state.Revision {
		return config.ErrConflict
	}
	return nil
}
func (s *Store) insertVersion(ctx context.Context, tx *gorm.DB, state *config.State, v config.Version) error {
	return storageError(tx.WithContext(ctx).Create(&versionRow{ConfigID: state.ID,
		Number: v.Number, Content: v.Content, Format: v.Format, Description: v.Description,
		Action: v.Action, SourceVersion: v.SourceVersion, CreatedAt: v.CreatedAt.UnixMicro()}).Error)
}

func (s *Store) publish(ctx context.Context, tx *gorm.DB, state *config.State, seq int64, deleted bool) (int64, error) {
	seq++
	if err := tx.WithContext(ctx).Model(&streamRow{}).Where(map[string]any{"id": 1}).Update("sequence", seq).Error; err != nil {
		return 0, err
	}
	if err := tx.Create(&eventRow{Sequence: seq, ConfigID: state.ID, Namespace: state.Key.Namespace,
		GroupName: state.Key.Group, Name: state.Key.Name, Deleted: deleted, CreatedAt: time.Now().UnixMicro()}).Error; err != nil {
		return 0, err
	}
	state.Sequence = seq
	return seq, tx.Commit().Error
}
func (s *Store) Save(ctx context.Context, k config.Key, edit config.Edit) (config.Mutation, error) {
	if err := k.Validate(); err != nil {
		return config.Mutation{}, err
	}
	if edit.Target != "" && edit.Target != "global" && edit.Target != "beta" {
		return config.Mutation{}, fmt.Errorf("%w: target must be global or beta", config.ErrInvalid)
	}
	if err := config.ValidateContent(edit.Format, edit.Content); err != nil {
		return config.Mutation{}, err
	}
	if len(edit.Description) > 4096 {
		return config.Mutation{}, fmt.Errorf("%w: description exceeds 4096 bytes", config.ErrInvalid)
	}
	tx, seq, err := s.beginMutation(ctx)
	if err != nil {
		return config.Mutation{}, err
	}
	defer tx.Rollback()
	state, err := s.load(ctx, tx, k)
	if errors.Is(err, config.ErrNotFound) {
		if edit.ExpectedID != "" || edit.ExpectedRevision != 0 || edit.Target == "beta" {
			return config.Mutation{}, config.ErrConflict
		}
		state = &config.State{ID: uuid.NewString(), Key: k, Revision: 1, LastVersion: 1, GlobalVersion: 1, Rules: []config.Rule{}, Versions: map[int64]config.Version{}}
		err = tx.Create(&configRow{ID: state.ID, Namespace: k.Namespace, GroupName: k.Group, Name: k.Name, Revision: 1, LastVersion: 1, GlobalVersion: 1}).Error
		if err != nil {
			return config.Mutation{}, storageError(err)
		}
	} else {
		if err != nil {
			return config.Mutation{}, err
		}
		if err = checkEdit(state, edit.ExpectedID, edit.ExpectedRevision); err != nil {
			return config.Mutation{}, err
		}

		if edit.Target == "beta" {
			if state.Beta == nil {
				return config.Mutation{}, config.ErrNotFound
			}
			beta := state.Beta
			if beta.Content == edit.Content && beta.Format == edit.Format && beta.Description == edit.Description {
				return config.Mutation{State: state}, nil
			}
			beta.Content, beta.Format, beta.Description = edit.Content, edit.Format, edit.Description
			beta.UpdatedAt = time.Now().UTC()
			state.Revision++
			if err = s.persistTargets(ctx, tx, state); err != nil {
				return config.Mutation{}, err
			}
			seq, err = s.publish(ctx, tx, state, seq, false)
			return config.Mutation{State: state, Changed: err == nil, Sequence: seq}, err
		}
		current := state.Versions[state.GlobalVersion]
		if current.Content == edit.Content && current.Format == edit.Format {
			return config.Mutation{State: state}, nil
		}
		state.LastVersion++
		state.Revision++
		state.GlobalVersion = state.LastVersion
	}
	action := "save"
	v := config.Version{Number: state.LastVersion, Content: edit.Content, Format: edit.Format, Description: edit.Description, Action: action, CreatedAt: time.Now().UTC()}
	if err = s.insertVersion(ctx, tx, state, v); err != nil {
		return config.Mutation{}, err
	}
	state.Versions[v.Number] = v
	if err = s.persistTargets(ctx, tx, state); err != nil {
		return config.Mutation{}, err
	}
	seq, err = s.publish(ctx, tx, state, seq, false)
	return config.Mutation{State: state, Changed: err == nil, Sequence: seq}, err
}
func (s *Store) persistTargets(ctx context.Context, tx *gorm.DB, state *config.State) error {
	tx = tx.WithContext(ctx)
	if err := tx.Model(&configRow{}).Where(map[string]any{"id": state.ID}).Updates(map[string]any{
		"revision": state.Revision, "last_version": state.LastVersion, "global_version": state.GlobalVersion,
	}).Error; err != nil {
		return err
	}
	if err := tx.Where(map[string]any{"config_id": state.ID}).Delete(&betaRow{}).Error; err != nil {
		return err
	}
	if state.Beta != nil {
		raw, err := json.Marshal(state.Beta)
		if err != nil {
			return err
		}
		if err = tx.Create(&betaRow{ConfigID: state.ID, BetaJSON: string(raw)}).Error; err != nil {
			return err
		}
	}
	if err := tx.Where(map[string]any{"config_id": state.ID}).Delete(&ruleRow{}).Error; err != nil {
		return err
	}
	rows := make([]ruleRow, 0, len(state.Rules))
	for i, rule := range state.Rules {
		raw, err := json.Marshal(rule)
		if err != nil {
			return err
		}
		rows = append(rows, ruleRow{ConfigID: state.ID, ID: rule.ID, Position: i, RuleJSON: string(raw)})
	}
	if len(rows) > 0 {
		return storageError(tx.Create(&rows).Error)
	}
	return nil
}
