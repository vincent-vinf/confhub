package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/configarchive"
)

func TestImportPublishesAllOrNoneWithPreviewConcurrency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	items := []configarchive.Item{{Group: "NEW", Name: "a", Format: "text", Content: "one"}, {Group: "NEW", Name: "b", Format: "text", Content: "two"}}
	p := configarchive.Package{Digest: "fixture", Items: items}
	plan, err := s.PreviewImport(ctx, "public", "ABORT", p, true)
	if err != nil {
		t.Fatal(err)
	}
	// Another writer claims the second name after preview. The first write and
	// group creation must not survive failure at the second configuration.
	if err = s.CreateGroup(ctx, "public", "NEW"); err != nil {
		t.Fatal(err)
	}
	b := config.Key{Namespace: "public", Group: "NEW", Name: "b"}
	if _, err = s.Save(ctx, b, config.Edit{Format: "text", Content: "concurrent"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Import(ctx, p, plan, true); !errors.Is(err, config.ErrConflict) {
		t.Fatalf("concurrent import: %v", err)
	}
	if _, err = s.Snapshot(ctx, config.Key{Namespace: "public", Group: "NEW", Name: "a"}); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("partial commit: %v", err)
	}
	plan, err = s.PreviewImport(ctx, "public", "OVERWRITE", p, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(ctx, p, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Updated != 1 {
		t.Fatalf("result: %+v", result)
	}
	state, err := s.Snapshot(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if state.GlobalVersion != 2 || config.Resolve(state, nil).Content != "two" {
		t.Fatal("overwrite not published")
	}
}

func TestExportUsesCurrentGlobalAndLiteralSearch(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "a_%"}
	m, err := s.Save(ctx, k, config.Edit{Content: "first", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "current\r\n", Format: "text", Description: "export description"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ExportArchive(ctx, "public", "DEFAULT_GROUP", nil, "_%", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "current\r\n" || items[0].Description != "export description" {
		t.Fatalf("export: %+v", items)
	}
	if _, err = s.ExportArchive(ctx, "public", "DEFAULT_GROUP", []string{"missing"}, "", false); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("missing selected: %v", err)
	}
}

func TestImportRollsBackNewGroupsAndEventsAfterLaterConflict(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "existing"}
	initial, err := s.Save(ctx, k, config.Edit{Content: "initial", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	p := configarchive.Package{Digest: "fixture", Items: []configarchive.Item{{Group: "NEW", Name: "a", Format: "text", Content: "new"}, {Group: k.Group, Name: k.Name, Format: "text", Content: "import"}}}
	plan, err := s.PreviewImport(ctx, "public", "OVERWRITE", p, true)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := s.Save(ctx, k, config.Edit{ExpectedID: initial.State.ID, ExpectedRevision: initial.State.Revision, Content: "concurrent", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Import(ctx, p, plan, true); !errors.Is(err, config.ErrConflict) {
		t.Fatal("stale preview", err)
	}
	groups, err := s.Groups(ctx, "public")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g == "NEW" {
			t.Fatal("group survived rollback")
		}
	}
	changes, err := s.Changes(ctx, changed.Sequence, 100)
	if err != nil || len(changes.Events) != 0 || changes.Sequence != changed.Sequence {
		t.Fatal("events leaked", changes, err)
	}
	if _, err = s.Snapshot(ctx, config.Key{Namespace: "public", Group: "NEW", Name: "a"}); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("config survived rollback", err)
	}
}

func TestImportPoliciesNoopAndBetaPreservation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "existing"}
	m, err := s.Save(ctx, k, config.Edit{Content: "initial", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "r", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	p := configarchive.Package{Digest: "fixture", Items: []configarchive.Item{{Group: k.Group, Name: k.Name, Format: "text", Content: "new", Description: "imported"}, {Group: k.Group, Name: "new", Format: "text", Content: "created"}}}
	plan, err := s.PreviewImport(ctx, "public", "ABORT", p, false)
	if err != nil || plan.CanImport {
		t.Fatal("abort preview", plan, err)
	}
	if _, err = s.Import(ctx, p, plan, false); !errors.Is(err, config.ErrConflict) {
		t.Fatal("abort", err)
	}
	plan, err = s.PreviewImport(ctx, "public", "SKIP", p, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(ctx, p, plan, false)
	if err != nil || result.Created != 1 || result.Skipped != 1 {
		t.Fatal("skip", result, err)
	}
	plan, err = s.PreviewImport(ctx, "public", "OVERWRITE", p, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.Import(ctx, p, plan, false)
	if err != nil || result.Updated != 1 || result.Unchanged != 1 {
		t.Fatal("overwrite", result, err)
	}
	state, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if config.Resolve(state, nil).Content != "new" || config.Resolve(state, map[string]string{"env": "gray"}).Content != "initial" || state.GlobalVersion != 2 || state.Versions[2].Description != "imported" {
		t.Fatal("beta/global changed unexpectedly", state)
	}
	seq := state.Sequence
	p.Items[0].Description = "description only"
	plan, err = s.PreviewImport(ctx, "public", "OVERWRITE", p, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err = s.Import(ctx, p, plan, false)
	if err != nil || result.Unchanged != 2 || result.Sequence != seq {
		t.Fatal("noop", result, err)
	}
	p.Items[0].Group = "MISSING"
	blocked, err := s.PreviewImport(ctx, "public", "OVERWRITE", p, false)
	if err != nil || blocked.CanImport || blocked.Entries[0].Action != "error" {
		t.Fatal("ordinary writer missing group not blocked", blocked, err)
	}
}

func TestConcurrentImportsHaveOneWinnerAndStaleRecreationConflicts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := configarchive.Package{Digest: "fixture", Items: []configarchive.Item{{Group: "DEFAULT_GROUP", Name: "a", Format: "text", Content: "one"}, {Group: "DEFAULT_GROUP", Name: "b", Format: "text", Content: "two"}}}
	plan, err := s.PreviewImport(ctx, "public", "ABORT", p, true)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 8)
	var done sync.WaitGroup
	for i := 0; i < 8; i++ {
		done.Add(1)
		go func() { defer done.Done(); <-start; _, e := s.Import(ctx, p, plan, true); results <- e }()
	}
	close(start)
	done.Wait()
	close(results)
	winners := 0
	for e := range results {
		if e == nil {
			winners++
		} else if !errors.Is(e, config.ErrConflict) {
			t.Fatal(e)
		}
	}
	if winners != 1 {
		t.Fatalf("winners %d", winners)
	}
	changes, err := s.Changes(ctx, 0, 100)
	if err != nil || len(changes.Events) != 2 {
		t.Fatal("losing imports leaked events", changes, err)
	}
	plan, err = s.PreviewImport(ctx, "public", "OVERWRITE", p, true)
	if err != nil {
		t.Fatal(err)
	}
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "b"}
	old, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete(ctx, k, old.ID, old.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(ctx, k, config.Edit{Content: "rebuilt", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Import(ctx, p, plan, true); !errors.Is(err, config.ErrConflict) {
		t.Fatal("recreated identity accepted", err)
	}
}

func TestImportPreviewListsAllItemErrorsWithoutWriting(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p := configarchive.Package{Digest: "fixture", Items: []configarchive.Item{{Group: "DEFAULT_GROUP", Name: "bad-json", Format: "json", Content: "{"}, {Group: "DEFAULT_GROUP", Name: "bad-type", Format: "unknown", Content: "x"}, {Group: "MISSING", Name: "new", Format: "text", Content: "x"}, {Group: "DEFAULT_GROUP", Name: "valid", Format: "text", Content: "x"}}}
	plan, err := s.PreviewImport(ctx, "public", "ABORT", p, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanImport || len(plan.Entries) != 4 {
		t.Fatalf("incomplete preview %+v", plan)
	}
	for i := 0; i < 3; i++ {
		if plan.Entries[i].Action != "error" || plan.Entries[i].Error == "" {
			t.Fatalf("missing item error %+v", plan.Entries[i])
		}
	}
	if plan.Entries[3].Action != "create" {
		t.Fatal("valid item omitted")
	}
	if _, err = s.Import(ctx, p, plan, true); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("invalid preview committed", err)
	}
	rows, err := s.List(ctx, "public", "DEFAULT_GROUP", "", 100)
	if err != nil || len(rows) != 0 {
		t.Fatal("preview wrote data", err)
	}
}

func TestExportAcrossGroupsKeepsSameNamesAndSelection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, group := range []string{"A", "B", "EMPTY"} {
		if err := s.CreateGroup(ctx, "public", group); err != nil {
			t.Fatal(err)
		}
		if group == "EMPTY" {
			continue
		}
		_, err := s.Save(ctx, config.Key{Namespace: "public", Group: group, Name: "same.yaml"}, config.Edit{Content: "value: " + group, Format: "yaml"})
		if err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ExportArchiveGroups(ctx, "public", map[string][]string{"A": nil, "B": nil, "EMPTY": nil}, "same", false)
	if err != nil || len(items) != 2 || items[0].Group != "A" || items[1].Group != "B" {
		t.Fatalf("groups: %+v %v", items, err)
	}
	items, err = s.ExportArchiveGroups(ctx, "public", map[string][]string{"B": {"same.yaml"}}, "", false)
	if err != nil || len(items) != 1 || items[0].Group != "B" {
		t.Fatalf("selection: %+v %v", items, err)
	}
	if _, err = s.ExportArchiveGroups(ctx, "public", map[string][]string{"A": {"same.yaml"}, "B": {"missing"}}, "", false); !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("missing selection: %v", err)
	}
	rows, err := s.List(ctx, "public", "A", "", 25)
	if err != nil || len(rows) != 1 || rows[0].ID == "" || rows[0].GlobalVersion != 1 || rows[0].PublishedAt.IsZero() {
		t.Fatalf("summary: %+v %v", rows, err)
	}
	state, err := s.Snapshot(ctx, rows[0].Key)
	if err != nil || !rows[0].PublishedAt.Equal(state.Versions[1].CreatedAt) {
		t.Fatalf("publication timestamp: %+v %v", rows, err)
	}
}
