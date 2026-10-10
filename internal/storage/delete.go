package storage

import (
	"context"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func (s *Store) Delete(ctx context.Context, k config.Key, id string, revision int64) (config.Mutation, error) {
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
	if err = tx.Where(map[string]any{"config_id": state.ID}).Delete(&ruleRow{}).Error; err != nil {
		return config.Mutation{}, err
	}
	if err = tx.Where(map[string]any{"id": state.ID}).Delete(&configRow{}).Error; err != nil {
		return config.Mutation{}, err
	}
	seq, err = s.publish(ctx, tx, state, seq, true)
	return config.Mutation{State: state, Changed: err == nil, Sequence: seq}, err
}
