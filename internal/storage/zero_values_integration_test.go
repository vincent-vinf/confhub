package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/clientinfo"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestEmptyBodiesAndDescriptionsSurviveMainAndBetaPublication(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "original", Format: "text", Description: "original description"})
	if err != nil {
		t.Fatal(err)
	}
	rules := []config.Rule{{ID: "r", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Format: "text"})
	if err != nil || !m.Changed {
		t.Fatal("clearing beta failed", m, err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Format: "text"})
	if err != nil || !m.Changed {
		t.Fatal("publishing empty main failed", m, err)
	}
	state, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if state.GlobalVersion != 2 || state.LastVersion != 2 || state.Beta == nil || state.Beta.Content != "" || state.Beta.Description != "" {
		t.Fatalf("empty beta or version numbering was lost: %+v", state)
	}
	for _, tc := range []struct {
		number               int64
		content, description string
	}{{1, "original", "original description"}, {2, "", ""}} {
		v, err := s.Version(ctx, k, tc.number)
		if err != nil || v.Content != tc.content || v.Description != tc.description || v.Format != "text" || v.SourceVersion != 0 || v.CreatedAt.IsZero() {
			t.Fatalf("version %d: %+v %v", tc.number, v, err)
		}
	}
}

func TestPresenceLaterBatchFailureRollsBackEarlierWritesAndRemovals(t *testing.T) {
	s := testutil.Store(t)
	ctx := context.Background()
	previous := make([]clientinfo.Client, 55)
	for i := range previous {
		previous[i] = clientinfo.Client{ID: fmt.Sprintf("client-%02d", i), Tags: map[string]string{"env": "old"}}
	}
	if err := s.SyncPresence(ctx, "node", previous, time.Minute); err != nil {
		t.Fatal(err)
	}
	updated := make([]clientinfo.Client, 60)
	for i := range updated {
		updated[i] = clientinfo.Client{ID: fmt.Sprintf("client-%02d", i), Tags: map[string]string{"env": "new"}}
	}
	// The duplicate occurs after at least one bounded write batch has flushed.
	invalid := append(append([]clientinfo.Client(nil), updated...), updated[0])
	if err := s.SyncPresence(ctx, "node", invalid, time.Minute); !errors.Is(err, config.ErrInvalid) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	page, err := s.Clients(ctx, "", 100)
	if err != nil || len(page.Clients) != 55 {
		t.Fatal("partial batch survived rollback", page, err)
	}
	for _, c := range page.Clients {
		if c.Tags["env"] != "old" {
			t.Fatal("earlier update survived failed transaction", c)
		}
	}
	if err = s.SyncPresence(ctx, "node", updated, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncPresence(ctx, "node", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	page, err = s.Clients(ctx, "", 100)
	if err != nil || len(page.Clients) != 0 {
		t.Fatal("batch removals did not clear clients", page, err)
	}
	values, err := s.TagSuggestions(ctx, "env", "")
	if err != nil || len(values) != 0 {
		t.Fatal("removed clients retained tag suggestions", values, err)
	}
}
