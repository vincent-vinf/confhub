package storage_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
)

func batchTargets(states ...*config.State) []storage.ConfigTarget {
	result := make([]storage.ConfigTarget, 0, len(states))
	for _, state := range states {
		result = append(result, storage.ConfigTarget{Key: state.Key, ExpectedID: state.ID, ExpectedRevision: state.Revision})
	}
	return result
}

func TestBatchMovePreservesHistoryBetaAndPublishesBothKeys(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.CreateNamespace(ctx, "destination"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, "destination", "apps"); err != nil {
		t.Fatal(err)
	}
	k := key()
	first, err := s.Save(ctx, k, config.Edit{Content: "old", Format: "text", Description: "first"})
	if err != nil {
		t.Fatal(err)
	}
	first, err = s.Save(ctx, k, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Content: "current", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	first, err = s.SetRules(ctx, k, first.State.ID, first.State.Revision, []config.Rule{{ID: "gray", Name: "gray", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err = s.Save(ctx, k, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Target: "beta", Content: "canary", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Save(ctx, key(), config.Edit{Content: "second", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	originals := []*config.State{first.State, second.State}
	moved, err := s.MoveMany(ctx, batchTargets(originals...), "destination", "apps")
	if err != nil || moved.Count != 2 || moved.Sequence != second.Sequence+4 {
		t.Fatal("batch move", moved, err)
	}
	var destinations []*config.State
	for _, original := range originals {
		if _, err := s.Snapshot(ctx, original.Key); !errors.Is(err, config.ErrNotFound) {
			t.Fatal("source remained", err)
		}
		newKey := config.Key{Namespace: "destination", Group: "apps", Name: original.Key.Name}
		current, err := s.Snapshot(ctx, newKey)
		if err != nil {
			t.Fatal(err)
		}
		if current.ID != original.ID || current.Revision != original.Revision+1 || current.GlobalVersion != original.GlobalVersion || current.LastVersion != original.LastVersion || !reflect.DeepEqual(current.Beta, original.Beta) || !reflect.DeepEqual(current.Rules, original.Rules) {
			t.Fatal("move altered configuration state", current, original)
		}
		destinations = append(destinations, current)
	}
	v, err := s.Version(ctx, destinations[0].Key, 1)
	if err != nil || v.Content != "old" || v.Description != "first" {
		t.Fatal("history lost", v, err)
	}
	changes, err := s.Changes(ctx, second.Sequence, 100)
	if err != nil || len(changes.Events) != 4 {
		t.Fatal("move notifications", changes, err)
	}
	for i, original := range originals {
		old, new := changes.Events[2*i], changes.Events[2*i+1]
		if old.Key != original.Key || !old.Deleted || new.Key != destinations[i].Key || new.Deleted || new.ID != original.ID || old.ID != original.ID {
			t.Fatal("incorrect key notifications", old, new)
		}
	}
	deleted, err := s.DeleteMany(ctx, batchTargets(destinations...))
	if err != nil || deleted.Count != 2 || deleted.Sequence != moved.Sequence+2 {
		t.Fatal("batch delete", deleted, err)
	}
	for _, state := range destinations {
		if _, err := s.Snapshot(ctx, state.Key); !errors.Is(err, config.ErrNotFound) {
			t.Fatal("configuration survived deletion", err)
		}
		if _, err := s.Version(ctx, state.Key, 1); !errors.Is(err, config.ErrNotFound) {
			t.Fatal("history survived deletion", err)
		}
	}
}

func TestBatchFailureRollsBackEarlierWritesAndEvents(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.CreateGroup(ctx, "public", "target"); err != nil {
		t.Fatal(err)
	}
	first, err := s.Save(ctx, key(), config.Edit{Content: "first", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Save(ctx, key(), config.Edit{Content: "second", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	collisionKey := config.Key{Namespace: "public", Group: "target", Name: second.State.Key.Name}
	collision, err := s.Save(ctx, collisionKey, config.Edit{Content: "keep", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	targets := batchTargets(first.State, second.State)
	if _, err := s.MoveMany(ctx, targets, "public", "target"); !errors.Is(err, config.ErrConflict) {
		t.Fatal("collision overwritten", err)
	}
	stale := batchTargets(first.State, second.State)
	stale[1].ExpectedRevision++
	if _, err := s.DeleteMany(ctx, stale); !errors.Is(err, config.ErrConflict) {
		t.Fatal("stale deletion accepted", err)
	}
	if _, err := s.MoveMany(ctx, stale, "public", "target"); !errors.Is(err, config.ErrConflict) {
		t.Fatal("stale move accepted", err)
	}
	for _, original := range []*config.State{first.State, second.State, collision.State} {
		current, err := s.Snapshot(ctx, original.Key)
		if err != nil || current.Revision != original.Revision || current.ID != original.ID {
			t.Fatal("failed batch altered state", current, err)
		}
		if _, err := s.Version(ctx, original.Key, 1); err != nil {
			t.Fatal("failed deletion lost history", err)
		}
	}
	if _, err := s.Snapshot(ctx, config.Key{Namespace: "public", Group: "target", Name: first.State.Key.Name}); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("partial destination survived", err)
	}
	changes, err := s.Changes(ctx, collision.Sequence, 100)
	if err != nil || len(changes.Events) != 0 || changes.Sequence != collision.Sequence {
		t.Fatal("failed batch leaked events", changes, err)
	}
	if _, err := s.DeleteMany(ctx, nil); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("empty batch accepted", err)
	}
	if _, err := s.DeleteMany(ctx, batchTargets(first.State, first.State)); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("duplicate batch accepted", err)
	}
	if _, err := s.MoveMany(ctx, batchTargets(first.State), "public", "missing"); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("missing destination accepted", err)
	}
	if _, err := s.MoveMany(ctx, batchTargets(first.State), "public", "DEFAULT_GROUP"); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("unchanged destination accepted", err)
	}
}

func TestConcurrentMoveAndDeleteHaveOneWinner(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.CreateGroup(ctx, "public", "target"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Save(ctx, key(), config.Edit{Content: "value", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, move := range []bool{true, false} {
		wg.Add(1)
		go func(move bool) {
			defer wg.Done()
			var err error
			if move {
				_, err = s.MoveMany(ctx, batchTargets(m.State), "public", "target")
			} else {
				_, err = s.DeleteMany(ctx, batchTargets(m.State))
			}
			errs <- err
		}(move)
	}
	wg.Wait()
	close(errs)
	winners := 0
	for err := range errs {
		if err == nil {
			winners++
		} else if !errors.Is(err, config.ErrNotFound) {
			t.Fatal("unexpected losing mutation", err)
		}
	}
	if winners != 1 {
		t.Fatal("concurrent mutations had multiple winners", winners)
	}
}
