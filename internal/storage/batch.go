package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/vincent-vinf/confhub/internal/config"
)

type ConfigTarget struct {
	Key              config.Key `json:"key"`
	ExpectedID       string     `json:"expected_id"`
	ExpectedRevision int64      `json:"expected_revision"`
}

type BatchMutation struct {
	Count    int   `json:"count"`
	Sequence int64 `json:"sequence"`
}

func (s *Store) DeleteMany(ctx context.Context, targets []ConfigTarget) (BatchMutation, error) {
	return s.changeMany(ctx, targets, "", "", false)
}

func (s *Store) MoveMany(ctx context.Context, targets []ConfigTarget, namespace, group string) (BatchMutation, error) {
	if err := (config.Key{Namespace: namespace, Group: group, Name: "target"}).Validate(); err != nil {
		return BatchMutation{}, err
	}
	return s.changeMany(ctx, targets, namespace, group, true)
}

func (s *Store) changeMany(ctx context.Context, targets []ConfigTarget, namespace, group string, move bool) (BatchMutation, error) {
	if len(targets) == 0 || len(targets) > 200 {
		return BatchMutation{}, fmt.Errorf("%w: select 1–200 configurations", config.ErrInvalid)
	}
	seen := make(map[config.Key]bool, len(targets))
	for _, target := range targets {
		if err := target.Key.Validate(); err != nil {
			return BatchMutation{}, err
		}
		if seen[target.Key] {
			return BatchMutation{}, fmt.Errorf("%w: duplicate configuration", config.ErrInvalid)
		}
		seen[target.Key] = true
	}
	tx, seq, err := s.beginMutation(ctx)
	if err != nil {
		return BatchMutation{}, err
	}
	defer tx.Rollback()
	if move {
		var destination groupRow
		if err := tx.Where(map[string]any{"namespace": namespace, "name": group}).Take(&destination).Error; err != nil {
			return BatchMutation{}, storageError(err)
		}
	}
	for _, target := range targets {
		state, err := s.load(ctx, tx, target.Key)
		if err != nil {
			return BatchMutation{}, err
		}
		if err := checkEdit(state, target.ExpectedID, target.ExpectedRevision); err != nil {
			return BatchMutation{}, err
		}
		if move {
			key := config.Key{Namespace: namespace, Group: group, Name: state.Key.Name}
			if key == state.Key {
				return BatchMutation{}, fmt.Errorf("%w: destination must differ from source", config.ErrInvalid)
			}
			var existing configRow
			err := configKey(tx, key).Take(&existing).Error
			if err == nil {
				return BatchMutation{}, fmt.Errorf("%w: destination already contains configuration %q", config.ErrConflict, key.Name)
			}
			if !errors.Is(storageError(err), config.ErrNotFound) {
				return BatchMutation{}, err
			}
			if err := tx.Model(&configRow{}).Where(map[string]any{"id": state.ID}).Updates(map[string]any{
				"namespace": namespace, "group_name": group, "revision": state.Revision + 1,
			}).Error; err != nil {
				return BatchMutation{}, storageError(err)
			}
			// Old-key subscribers receive deletion; new-key subscribers receive
			// the same configuration identity and its complete effective state.
			seq, err = s.appendEvent(ctx, tx, state, seq, true)
			if err != nil {
				return BatchMutation{}, err
			}
			state.Key = key
			state.Revision++
			seq, err = s.appendEvent(ctx, tx, state, seq, false)
		} else {
			if err := tx.Where(map[string]any{"config_id": state.ID}).Delete(&ruleRow{}).Error; err != nil {
				return BatchMutation{}, err
			}
			if err := tx.Where(map[string]any{"id": state.ID}).Delete(&configRow{}).Error; err != nil {
				return BatchMutation{}, err
			}
			seq, err = s.appendEvent(ctx, tx, state, seq, true)
		}
		if err != nil {
			return BatchMutation{}, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return BatchMutation{}, err
	}
	return BatchMutation{Count: len(targets), Sequence: seq}, nil
}
