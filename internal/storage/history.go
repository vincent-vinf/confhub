package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

// CopyVersion performs rollback or promotion within the same transaction as
// the optimistic check and event. Source contents cannot be pruned concurrently.
func (s *Store) CopyVersion(ctx context.Context, k config.Key, id string, revision, source int64, ruleID string, rollback bool) (config.Mutation, error) {
	tx, seq, err := s.beginMutation(ctx)
	if err != nil {
		return config.Mutation{}, err
	}
	defer tx.Rollback()
	state, err := s.load(ctx, tx, k)
	if err != nil {
		return config.Mutation{}, err
	}
	if err = checkEdit(state, id, revision); err != nil {
		return config.Mutation{}, err
	}

	var v config.Version
	if rollback {
		if ruleID != "" || source < 1 {
			return config.Mutation{}, fmt.Errorf("%w: rollback only supports main versions", config.ErrInvalid)
		}
		v, err = scanVersion(tx.QueryRowContext(ctx, s.query("SELECT number,content,format,description,action,source_version,created_at FROM config_versions WHERE config_id=? AND number=?"), state.ID, source))
		if err != nil {
			return config.Mutation{}, err
		}
		v.Action = "rollback"
		v.Description = fmt.Sprintf("Rollback from version %d", source)
	} else {
		if ruleID == "" || source != 0 {
			return config.Mutation{}, fmt.Errorf("%w: promotion requires a gray rule, not a historical version", config.ErrInvalid)
		}
		found := false
		for _, r := range state.Rules {
			if r.ID == ruleID {
				v = config.Version{Content: r.Beta.Content, Format: r.Beta.Format, Description: r.Beta.Description, Action: "promote"}
				source = r.Beta.BaseVersion
				found = true
				break
			}
		}
		if !found {
			return config.Mutation{}, config.ErrNotFound
		}
	}
	current := state.Versions[state.GlobalVersion]
	if current.Content == v.Content && current.Format == v.Format {
		return config.Mutation{State: state}, nil
	}
	state.LastVersion++
	state.Revision++
	v.Number = state.LastVersion
	v.SourceVersion = source
	v.CreatedAt = time.Now().UTC()
	state.GlobalVersion = v.Number
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

type VersionPage struct {
	Versions   []config.Version `json:"versions"`
	NextBefore int64            `json:"next_before,omitempty"`
}

// History omits text bodies; the single-version endpoint supplies content for diff.
func (s *Store) History(ctx context.Context, k config.Key, before int64, limit int) (VersionPage, error) {
	if limit < 1 || limit > 100 {
		return VersionPage{}, fmt.Errorf("%w: limit must be 1–100", config.ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return VersionPage{}, err
	}
	defer tx.Rollback()
	state, err := s.load(ctx, tx, k)
	if err != nil {
		return VersionPage{}, err
	}
	if before <= 0 {
		before = state.LastVersion + 1
	}
	rows, err := tx.QueryContext(ctx, s.query("SELECT number,format,description,action,source_version,created_at FROM config_versions WHERE config_id=? AND number<? ORDER BY number DESC LIMIT ?"), state.ID, before, limit)
	if err != nil {
		return VersionPage{}, err
	}
	page := VersionPage{Versions: []config.Version{}}
	for rows.Next() {
		var v config.Version
		var created int64
		if err = rows.Scan(&v.Number, &v.Format, &v.Description, &v.Action, &v.SourceVersion, &created); err != nil {
			break
		}
		v.CreatedAt = time.UnixMicro(created).UTC()
		if v.Number == state.GlobalVersion {
			v.References = append(v.References, "global")
		}
		page.Versions = append(page.Versions, v)
	}
	if err = errorsJoinRows(err, rows); err != nil {
		return VersionPage{}, err
	}
	if len(page.Versions) == limit {
		page.NextBefore = page.Versions[len(page.Versions)-1].Number
	}
	return page, tx.Commit()
}
func errorsJoinRows(err error, rows *sql.Rows) error {
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
