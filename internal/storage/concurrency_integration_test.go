package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/storage"
	"gitlab.bodesitech.com/bodesi/confhub/internal/testutil"
)

func TestThirtyTwoBetaEditorsHaveOneWinnerAndOneEvent(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	k := key()
	m, err := s.Save(ctx, k, config.Edit{Content: "initial", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "r", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(32)
	done.Add(32)
	type result struct {
		value config.Mutation
		err   error
	}
	results := make(chan result, 32)
	for i := 0; i < 32; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			<-start
			value, err := s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Content: fmt.Sprintf("writer-%d", i), Format: "text"})
			results <- result{value, err}
		}(i)
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(results)
	success := 0
	winner := ""
	for result := range results {
		if result.err == nil {
			success++
			winner = result.value.State.Beta.Content
		} else if !errors.Is(result.err, config.ErrConflict) {
			t.Fatal(result.err)
		}
	}
	if success != 1 {
		t.Fatalf("winners=%d", success)
	}
	current, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if current.LastVersion != 1 || current.Revision != m.State.Revision+1 || current.Beta.Content != winner {
		t.Fatal("lost beta or consumed main versions", current)
	}
	changes, err := s.Changes(ctx, m.Sequence, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Events) != 1 || changes.Events[0].Key != k {
		t.Fatal("losing writes leaked events", changes)
	}
}

func TestEventWriteFailureRollsBackAllConfigurationChanges(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	// SQL is used only to inject a failure at the real database boundary.
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	k := key()
	initial, err := s.Save(ctx, k, config.Edit{Content: "initial", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE FUNCTION fail_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected event write failure'; END $$; CREATE TRIGGER fail_event BEFORE INSERT ON change_events FOR EACH ROW EXECUTE FUNCTION fail_event()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Save(ctx, k, config.Edit{ExpectedID: initial.State.ID, ExpectedRevision: initial.State.Revision, Content: "uncommitted", Format: "text"})
	if err == nil {
		t.Fatal("injected failure did not fail publication")
	}
	current, err := s.Snapshot(ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != initial.State.Revision || current.LastVersion != 1 || current.Versions[1].Content != "initial" {
		t.Fatal("partial publication survived failed event", current)
	}
	if _, err = s.Version(ctx, k, 2); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("failed history survived", err)
	}
	changes, err := s.Changes(ctx, initial.Sequence, 100)
	if err != nil || len(changes.Events) != 0 || changes.Sequence != initial.Sequence {
		t.Fatal("failed transaction advanced change stream", changes, err)
	}
	_, err = db.Exec("DROP TRIGGER fail_event ON change_events")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Save(ctx, k, config.Edit{ExpectedID: initial.State.ID, ExpectedRevision: initial.State.Revision, Content: "committed", Format: "text"})
	if err != nil || next.State.LastVersion != 2 {
		t.Fatal("rollback prevented subsequent valid publication", next, err)
	}
}

func TestCleanupRacingHistoricalCopyHasOnlyAtomicOutcomes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, rollback := range []bool{false, true} {
		k := key()
		m, err := s.Save(ctx, k, config.Edit{Content: "source", Format: "text"})
		if err != nil {
			t.Fatal(err)
		}
		m, err = s.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "current", Format: "text"})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		copied := make(chan error, 1)
		cleaned := make(chan error, 1)
		go func() { <-start; _, err := s.Cleanup(ctx, "copy-cleanup", 1, time.Hour, 256); cleaned <- err }()
		go func() {
			<-start
			var err error
			if rollback {
				_, err = s.CopyVersion(ctx, k, m.State.ID, m.State.Revision, 1, "", true)
			} else {
				_, err = s.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "r", Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}, 1)
			}
			copied <- err
		}()
		close(start)
		copyErr, cleanupErr := <-copied, <-cleaned
		if cleanupErr != nil {
			t.Fatal(cleanupErr)
		}
		current, err := s.Snapshot(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if copyErr == nil {
			if rollback {
				if current.LastVersion != 3 || current.Versions[3].Content != "source" {
					t.Fatal("partial rollback", current)
				}
			} else {
				if current.Beta == nil || current.Beta.Content != "source" || len(current.Rules) != 1 {
					t.Fatal("partial beta copy", current)
				}
			}
		} else {
			if !errors.Is(copyErr, config.ErrNotFound) {
				t.Fatal(copyErr)
			}
			if current.Revision != m.State.Revision || current.LastVersion != 2 || current.Beta != nil || len(current.Rules) != 0 {
				t.Fatal("failed source copy mutated state", current)
			}
		}
	}
}
