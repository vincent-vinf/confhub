package storage

import (
	"context"
	"encoding/json"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

// SetRules atomically replaces the ordered rule list. A disabled rule still
// retains its historical target so re-enabling cannot reference pruned content.
func (s *Store) SetRules(ctx context.Context, k config.Key, id string, revision int64, rules []config.Rule) (config.Mutation, error) {
	if err := config.ValidateRules(rules); err != nil {
		return config.Mutation{}, err
	}
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
	before, _ := json.Marshal(state.Rules)
	after, _ := json.Marshal(rules)
	if string(before) == string(after) {
		return config.Mutation{State: state}, nil
	}
	for _, r := range rules {
		if _, ok := state.Versions[r.TargetVersion]; ok {
			continue
		}
		v, err := scanVersion(tx.QueryRowContext(ctx, s.query("SELECT number,content,format,description,action,source_version,created_at FROM config_versions WHERE config_id=? AND number=?"), state.ID, r.TargetVersion))
		if err != nil {
			return config.Mutation{}, err
		}
		state.Versions[v.Number] = v
	}
	state.Rules = rules
	state.Revision++
	if err = s.persistTargets(ctx, tx, state); err != nil {
		return config.Mutation{}, err
	}
	seq, err = s.publish(ctx, tx, state, seq, false)
	return config.Mutation{State: state, Changed: err == nil, Sequence: seq}, err
}
