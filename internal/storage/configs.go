package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysqlsql "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

type reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func storageError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return config.ErrNotFound
	}
	var pg *pq.Error
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return config.ErrConflict
		case "23503":
			return fmt.Errorf("%w: referenced organization or version missing", config.ErrInvalid)
		}
	}
	var my *mysqlsql.MySQLError
	if errors.As(err, &my) {
		switch my.Number {
		case 1062:
			return config.ErrConflict
		case 1451, 1452:
			return fmt.Errorf("%w: referenced organization or version missing", config.ErrInvalid)
		}
	}
	return err
}
func (s *Store) load(ctx context.Context, r reader, k config.Key) (*config.State, error) {
	state := &config.State{Key: k, Rules: []config.Rule{}, Versions: map[int64]config.Version{}}
	err := r.QueryRowContext(ctx, s.query("SELECT id,revision,last_version,global_version FROM configs WHERE namespace=? AND group_name=? AND name=?"), k.Namespace, k.Group, k.Name).Scan(&state.ID, &state.Revision, &state.LastVersion, &state.GlobalVersion)
	if err != nil {
		return nil, storageError(err)
	}
	var betaRaw string
	err = r.QueryRowContext(ctx, s.query("SELECT beta_json FROM config_beta WHERE config_id=?"), state.ID).Scan(&betaRaw)
	if err == nil {
		state.Beta = &config.Beta{}
		if err = json.Unmarshal([]byte(betaRaw), state.Beta); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := r.QueryContext(ctx, s.query("SELECT rule_json FROM gray_rules WHERE config_id=? ORDER BY position"), state.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var rule config.Rule
		if err = json.Unmarshal([]byte(raw), &rule); err != nil {
			break
		}
		state.Rules = append(state.Rules, rule)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	rows, err = r.QueryContext(ctx, s.query("SELECT number,content,format,description,action,source_version,created_at FROM config_versions WHERE config_id=? AND number=?"), state.ID, state.GlobalVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanVersion(rows)
		if e != nil {
			return nil, e
		}
		state.Versions[v.Number] = v
	}
	return state, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanVersion(row scanner) (config.Version, error) {
	var v config.Version
	var created int64
	err := row.Scan(&v.Number, &v.Content, &v.Format, &v.Description, &v.Action, &v.SourceVersion, &created)
	v.CreatedAt = time.UnixMicro(created).UTC()
	return v, storageError(err)
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var sequence int64
	if err = tx.QueryRowContext(ctx, "SELECT sequence FROM change_stream WHERE id=1").Scan(&sequence); err != nil {
		return nil, 0, err
	}
	state, readErr := s.load(ctx, tx, k)
	if readErr != nil && !errors.Is(readErr, config.ErrNotFound) {
		return nil, 0, readErr
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, err
	}
	if state != nil {
		state.Sequence = sequence
	}
	return state, sequence, readErr
}
func (s *Store) Version(ctx context.Context, k config.Key, number int64) (config.Version, error) {
	return scanVersion(s.db.QueryRowContext(ctx, s.query("SELECT v.number,v.content,v.format,v.description,v.action,v.source_version,v.created_at FROM config_versions v JOIN configs c ON c.id=v.config_id WHERE c.namespace=? AND c.group_name=? AND c.name=? AND v.number=?"), k.Namespace, k.Group, k.Name, number))
}
func (s *Store) beginMutation(ctx context.Context) (*sql.Tx, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, 0, err
	}
	var seq int64
	err = tx.QueryRowContext(ctx, "SELECT sequence FROM change_stream WHERE id=1 FOR UPDATE").Scan(&seq)
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	return tx, seq, nil
}
func checkEdit(state *config.State, id string, revision int64) error {
	if id != state.ID || revision != state.Revision {
		return config.ErrConflict
	}
	return nil
}
func (s *Store) insertVersion(ctx context.Context, tx *sql.Tx, state *config.State, v config.Version) error {
	_, err := tx.ExecContext(ctx, s.query("INSERT INTO config_versions(config_id,number,content,format,description,action,source_version,created_at) VALUES (?,?,?,?,?,?,?,?)"), state.ID, v.Number, v.Content, v.Format, v.Description, v.Action, v.SourceVersion, v.CreatedAt.UnixMicro())
	return storageError(err)
}
func (s *Store) publish(ctx context.Context, tx *sql.Tx, state *config.State, seq int64, deleted bool) (int64, error) {
	seq++
	_, err := tx.ExecContext(ctx, s.query("UPDATE change_stream SET sequence=? WHERE id=1"), seq)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, s.query("INSERT INTO change_events(sequence,config_id,namespace,group_name,name,deleted,created_at) VALUES (?,?,?,?,?,?,?)"), seq, state.ID, state.Key.Namespace, state.Key.Group, state.Key.Name, deleted, time.Now().UnixMicro())
	if err != nil {
		return 0, err
	}
	state.Sequence = seq
	return seq, tx.Commit()
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
		_, err = tx.ExecContext(ctx, s.query("INSERT INTO configs(id,namespace,group_name,name,revision,last_version,global_version) VALUES (?,?,?,?,?,?,?)"), state.ID, k.Namespace, k.Group, k.Name, 1, 1, 1)
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
func (s *Store) persistTargets(ctx context.Context, tx *sql.Tx, state *config.State) error {
	_, err := tx.ExecContext(ctx, s.query("UPDATE configs SET revision=?,last_version=?,global_version=? WHERE id=?"), state.Revision, state.LastVersion, state.GlobalVersion, state.ID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.query("DELETE FROM config_beta WHERE config_id=?"), state.ID); err != nil {
		return err
	}
	if state.Beta != nil {
		raw, e := json.Marshal(state.Beta)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, s.query("INSERT INTO config_beta(config_id,beta_json) VALUES(?,?)"), state.ID, string(raw)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.query("DELETE FROM gray_rules WHERE config_id=?"), state.ID)
	if err != nil {
		return err
	}
	for i, r := range state.Rules {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.query("INSERT INTO gray_rules(config_id,id,position,rule_json) VALUES (?,?,?,?)"), state.ID, r.ID, i, string(raw))
		if err != nil {
			return storageError(err)
		}
	}
	return nil
}
