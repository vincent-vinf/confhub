package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"gorm.io/gorm/clause"
)

// CopyVersion performs rollback or promotion within the same transaction as
// the optimistic check and event. Source contents cannot be pruned concurrently.
func (s *Store) CopyVersion(ctx context.Context, k config.Key, id string, revision, source int64, target string, rollback bool) (config.Mutation, error) {
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
		if target != "" || source < 1 {
			return config.Mutation{}, fmt.Errorf("%w: rollback only supports main versions", config.ErrInvalid)
		}
		v, err = loadVersion(tx, state.ID, source)
		if err != nil {
			return config.Mutation{}, err
		}
		v.Action = "rollback"
		v.Description = fmt.Sprintf("Rollback from version %d", source)
	} else {
		if target != "beta" || source != 0 {
			return config.Mutation{}, fmt.Errorf("%w: promotion copies beta, not a main version", config.ErrInvalid)
		}
		if state.Beta == nil {
			return config.Mutation{}, config.ErrNotFound
		}
		v = config.Version{Content: state.Beta.Content, Format: state.Beta.Format, Description: state.Beta.Description, Action: "promote"}
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
	tx := s.db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if tx.Error != nil {
		return VersionPage{}, tx.Error
	}
	defer tx.Rollback()
	state, err := s.load(ctx, tx, k)
	if err != nil {
		return VersionPage{}, err
	}
	if before <= 0 {
		before = state.LastVersion + 1
	}
	var rows []versionRow
	// Omit bodies at the database boundary; history pages only need metadata.
	err = tx.Select("number", "format", "description", "action", "source_version", "created_at").
		Where(map[string]any{"config_id": state.ID}).Where(clause.Lt{Column: "number", Value: before}).
		Order(clause.OrderByColumn{Column: clause.Column{Name: "number"}, Desc: true}).Limit(limit).Find(&rows).Error
	if err != nil {
		return VersionPage{}, err
	}
	page := VersionPage{Versions: make([]config.Version, 0, len(rows))}
	for _, row := range rows {
		v := row.version()
		if v.Number == state.GlobalVersion {
			v.References = append(v.References, "global")
		}
		page.Versions = append(page.Versions, v)
	}
	if len(page.Versions) == limit {
		page.NextBefore = page.Versions[len(page.Versions)-1].Number
	}
	return page, tx.Commit().Error
}
