package storage_test

import (
	"context"

	"errors"

	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/storage"
	"gitlab.bodesitech.com/bodesi/confhub/internal/testutil"
)

func testStore(t *testing.T) *storage.Store { return testutil.Store(t) }
func key() config.Key {
	return config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: uuid.NewString()}
}
func TestSavePublishesAnImmutableIncrementingVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	created, err := s.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if created.State.GlobalVersion != 1 || !created.Changed || created.Sequence == 0 {
		t.Fatalf("first publication: %+v", created)
	}
	first, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if config.Resolve(first, nil).Content != "one" {
		t.Fatal("publication not readable")
	}
	updated, err := s.Save(ctx, k, config.Edit{ExpectedID: first.ID, ExpectedRevision: first.Revision, Content: "two", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.GlobalVersion != 2 {
		t.Fatalf("version = %d", updated.State.GlobalVersion)
	}
	history, err := s.Version(ctx, k, 1)
	if err != nil {
		t.Fatal(err)
	}
	if history.Content != "one" {
		t.Fatal("historical version changed")
	}
	noop, err := s.Save(ctx, k, config.Edit{ExpectedID: updated.State.ID, ExpectedRevision: updated.State.Revision, Content: "two", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if noop.Changed || noop.State.LastVersion != 2 || noop.Sequence != 0 {
		t.Fatalf("identical text must be a no-op: %+v", noop)
	}
}

func TestGlobalPublicationLeavesGrayClientsPinned(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	rules := []config.Rule{{ID: "canary", Name: "canary", Enabled: true, TargetVersion: 1, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, rules)
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "two", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if config.Resolve(state, nil).Version != 2 || config.Resolve(state, map[string]string{"env": "gray"}).Version != 1 {
		t.Fatal("global save changed gray target")
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, RuleID: "canary", Content: "three", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if m.State.GlobalVersion != 2 || config.Resolve(m.State, map[string]string{"env": "gray"}).Version != 3 {
		t.Fatal("gray save changed global target")
	}
}

func TestRollbackCopiesHistoryIntoNewVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "two", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.CopyVersion(ctx, k, m.State.ID, m.State.Revision, 1, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if m.State.GlobalVersion != 3 || m.State.Versions[3].Content != "one" || m.State.Versions[3].SourceVersion != 1 || m.State.Versions[3].Description == "" {
		t.Fatalf("rollback: %+v", m.State)
	}
	original, err := s.Version(ctx, k, 1)
	if err != nil || original.Action != "save" {
		t.Fatal("rollback mutated source")
	}
}

func TestConcurrentEditorsCannotOverwriteEachOther(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	first, err := s.Save(ctx, k, config.Edit{Content: "first", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, text := range []string{"A", "B"} {
		wg.Add(1)
		go func(text string) {
			defer wg.Done()
			_, err := s.Save(ctx, k, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Content: text, Format: "text"})
			errs <- err
		}(text)
	}
	wg.Wait()
	close(errs)
	success, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, config.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	changes, err := s.Changes(ctx, first.Sequence, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range changes.Events {
		if event.Key == k {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("failed transaction leaked an event: %d", count)
	}
	current, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastVersion != 2 {
		t.Fatal("failed transaction consumed a version")
	}
}

func TestDeletionAndRecreationUseDifferentIdentity(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "old", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete(ctx, k, m.State.ID, m.State.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Snapshot(ctx, k); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("deleted config readable: %v", err)
	}
	if _, err = s.Version(ctx, k, 1); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("history survived permanent deletion")
	}
	next, err := s.Save(ctx, k, config.Edit{Content: "new", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if next.State.ID == m.State.ID || next.State.LastVersion != 1 {
		t.Fatal("recreation reused old identity")
	}
	if _, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: 1, Content: "late", Format: "text"}); !errors.Is(err, config.ErrConflict) {
		t.Fatal("late edit crossed identity boundary")
	}
}

func TestNonemptyOrganizationCannotBeDeleted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ns := uuid.NewString()
	if err := s.CreateNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGroup(ctx, ns, "apps"); err != nil {
		t.Fatal(err)
	}
	k := config.Key{Namespace: ns, Group: "apps", Name: "service"}
	m, err := s.Save(ctx, k, config.Edit{Content: "value", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteGroup(ctx, ns, "apps"); !errors.Is(err, config.ErrNotEmpty) {
		t.Fatalf("nonempty group deletion: %v", err)
	}
	if err = s.DeleteNamespace(ctx, ns); !errors.Is(err, config.ErrNotEmpty) {
		t.Fatalf("nonempty namespace deletion: %v", err)
	}
	if _, err = s.Delete(ctx, k, m.State.ID, m.State.Revision); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteGroup(ctx, ns, "apps"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupKeepsReferencedHistoryAndRecordsEventGap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "pinned", Enabled: false, TargetVersion: 1, Conditions: []config.Condition{{Tag: "a", Operator: "eq", Values: []string{"b"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"two", "three", "four"} {
		m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: text, Format: "text"})
		if err != nil {
			t.Fatal(err)
		}
	}
	acquired, err := s.Cleanup(ctx, "worker-a", 2, time.Nanosecond, 1000)
	if err != nil || !acquired {
		t.Fatalf("cleanup: %v %v", acquired, err)
	}
	if _, err = s.Version(ctx, k, 1); err != nil {
		t.Fatal("disabled rule's target was pruned")
	}
	if _, err = s.Version(ctx, k, 2); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("unreferenced old version retained")
	}
	for _, n := range []int64{3, 4} {
		if _, err = s.Version(ctx, k, n); err != nil {
			t.Fatal("recent history pruned")
		}
	}
	acquired, err = s.Cleanup(ctx, "worker-b", 2, time.Nanosecond, 1000)
	if err != nil || acquired {
		t.Fatal("two replicas obtained maintenance lease")
	}
	changes, err := s.Changes(ctx, 0, 100)
	if err != nil || changes.PurgedThrough == 0 {
		t.Fatalf("missing purge watermark: %+v %v", changes, err)
	}
}

func TestGrayRollbackAndPromotionLeaveOtherTargetsIntact(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "canary", Enabled: true, TargetVersion: 1, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "two", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "three", Format: "text", RuleID: "canary"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.CopyVersion(ctx, k, m.State.ID, m.State.Revision, 1, "canary", true)
	if err != nil {
		t.Fatal(err)
	}
	if m.State.GlobalVersion != 2 || m.State.Rules[0].TargetVersion != 4 || m.State.Versions[4].Action != "gray_rollback" {
		t.Fatal("gray rollback changed global target")
	}
	m, err = s.CopyVersion(ctx, k, m.State.ID, m.State.Revision, 4, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if m.State.GlobalVersion != 5 || m.State.Rules[0].TargetVersion != 4 || m.State.Versions[5].Content != "one" {
		t.Fatal("promotion changed gray rule")
	}
	page, err := s.History(ctx, k, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Versions) != 2 || page.NextBefore != 4 || len(page.Versions[0].References) != 1 || page.Versions[0].References[0] != "global" || page.Versions[1].References[0] != "canary" {
		t.Fatalf("history references: %+v", page)
	}
}
