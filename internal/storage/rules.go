package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"time"
)

// SetRules atomically replaces routing metadata. The first rule creates a single
// beta from the chosen historical main version; later rules share that content.
func (s *Store) SetRules(ctx context.Context, k config.Key, id string, revision int64, rules []config.Rule, source int64) (config.Mutation, error) {
	if err := k.Validate(); err != nil {
		return config.Mutation{}, err
	}
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
	if state.Beta == nil && len(rules) > 0 {
		if source < 1 {
			return config.Mutation{}, fmt.Errorf("%w: select a main version to initialize beta", config.ErrInvalid)
		}
		v, e := loadVersion(tx, state.ID, source)
		if e != nil {
			return config.Mutation{}, e
		}
		state.Beta = &config.Beta{Content: v.Content, Format: v.Format, Description: v.Description, UpdatedAt: time.Now().UTC()}
	} else if source != 0 {
		return config.Mutation{}, fmt.Errorf("%w: source_version is only allowed when creating the first beta", config.ErrInvalid)
	}
	before, _ := json.Marshal(state.Rules)
	if rules == nil {
		rules = []config.Rule{}
	}
	after, _ := json.Marshal(rules)
	if string(before) == string(after) {
		return config.Mutation{State: state}, nil
	}
	state.Rules = rules
	if len(rules) == 0 {
		state.Beta = nil
	}
	state.Revision++
	if err = s.persistTargets(ctx, tx, state); err != nil {
		return config.Mutation{}, err
	}
	seq, err = s.publish(ctx, tx, state, seq, false)
	return config.Mutation{State: state, Changed: err == nil, Sequence: seq}, err
}
