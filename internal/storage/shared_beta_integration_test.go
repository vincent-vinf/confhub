package storage_test

import (
	"context"
	"errors"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/testutil"
	"testing"
)

func TestSingleBetaCopiesSelectedHistoryAndIsSharedByEveryRule(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "selected-source", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "current-global", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	rules := []config.Rule{{ID: "a", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"a"}}}}, {ID: "b", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"b"}}}}}
	if _, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules, 0); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("source required", err)
	}
	if _, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules, 99); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("missing source", err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.State.Beta.Content != "selected-source" || m.State.LastVersion != 2 {
		t.Fatal(m.State)
	}
	stale := m.State.Revision
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Content: "shared-edit", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"a", "b"} {
		v := config.Resolve(m.State, map[string]string{"env": tag})
		if !v.Beta || v.Version != 0 || v.Content != "shared-edit" {
			t.Fatal(v)
		}
	}
	if m.State.LastVersion != 2 || config.Resolve(m.State, nil).Content != "current-global" {
		t.Fatal(m.State)
	}
	if _, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: stale, Target: "beta", Content: "stale", Format: "text"}); !errors.Is(err, config.ErrConflict) {
		t.Fatal(err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules[1:], 0)
	if err != nil || m.State.Beta.Content != "shared-edit" {
		t.Fatal(m, err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, nil, 0)
	if err != nil || m.State.Beta != nil {
		t.Fatal("last rule must delete beta", m, err)
	}
	if _, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Content: "gone", Format: "text"}); !errors.Is(err, config.ErrNotFound) {
		t.Fatal(err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules, 2)
	if err != nil || m.State.Beta.Content != "current-global" {
		t.Fatal("recreate from new selection", m, err)
	}
}
