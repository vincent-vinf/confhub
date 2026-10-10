package storage

import (
	"context"
	"encoding/json"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

// SetRules atomically replaces the ordered rule list. A disabled rule still
// retains its beta content. Only Save can edit beta, never metadata updates.
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

	// Ignore caller-supplied beta content; copy existing betas or initialize from
	// the current global content within this transaction.
	next := make([]config.Rule, len(rules))
	global := state.Versions[state.GlobalVersion]
	for i, r := range rules {
		r.Beta = config.Beta{BaseVersion: global.Number, Content: global.Content, Format: global.Format}
		for _, existing := range state.Rules {
			if existing.ID == r.ID {
				r.Beta = existing.Beta
				break
			}
		}
		next[i] = r
	}
	before, _ := json.Marshal(state.Rules)
	after, _ := json.Marshal(next)
	if string(before) == string(after) {
		return config.Mutation{State: state}, nil
	}
	rules = next
	state.Rules = rules
	state.Revision++
	if err = s.persistTargets(ctx, tx, state); err != nil {
		return config.Mutation{}, err
	}
	seq, err = s.publish(ctx, tx, state, seq, false)
	return config.Mutation{State: state, Changed: err == nil, Sequence: seq}, err
}
