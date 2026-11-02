package storage_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestFullMirrorImportDoesNotHoldLeaseWhileWritingSnapshot(t *testing.T) {
	cloud := testutil.Store(t)
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	edge, err := storage.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer edge.Close()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "staging"}
	if _, err := cloud.Save(context.Background(), key, config.Edit{Content: "new", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(context.Background(), replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := edge.AcquireMirrorLease(context.Background(), "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	blocker, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.Exec("LOCK TABLE configs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- edge.ApplyMirror(ctx, lease, batch, &data) }()
	defer func() { cancel(); blocker.Rollback(); <-done }()
	time.Sleep(100 * time.Millisecond)
	renewCtx, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	_, ok, err := edge.AcquireMirrorLease(renewCtx, "owner", time.Minute)
	if err != nil || !ok {
		t.Fatalf("snapshot writes blocked lease renewal: %v", err)
	}
}

func TestPrivateSnapshotStagingKeepsOldViewAndRenewsLease(t *testing.T) {
	for _, outcome := range []string{"complete", "cancel", "takeover"} {
		t.Run(outcome, func(t *testing.T) {
			cancelImport := outcome != "complete"
			cloud := testutil.Store(t)
			dsn := testutil.Database(t)
			if err := storage.Migrate("postgres", dsn, false); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			edge, err := storage.Open(ctx, "postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer edge.Close()
			key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "stable"}
			first, err := cloud.Save(ctx, key, config.Edit{Content: "old", Format: "text"})
			if err != nil {
				t.Fatal(err)
			}
			var initial bytes.Buffer
			base, err := cloud.Export(ctx, replication.Cursor{}, &initial)
			if err != nil {
				t.Fatal(err)
			}
			lease, _, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := edge.ApplyMirror(ctx, lease, base, &initial); err != nil {
				t.Fatal(err)
			}
			if _, err := cloud.Save(ctx, key, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Content: "new", Format: "text"}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 256; i++ {
				k := key
				k.Name = fmt.Sprintf("bulk-%03d", i)
				if _, err := cloud.Save(ctx, k, config.Edit{Content: "bulk", Format: "text"}); err != nil {
					t.Fatal(err)
				}
			}
			var data bytes.Buffer
			batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
			if err != nil {
				t.Fatal(err)
			}
			dec := replication.NewRecordDecoder(bytes.NewReader(data.Bytes()))
			var record replication.Record
			for i := 0; i <= 200; i++ {
				if err := dec.Decode(&record); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			lockID := time.Now().UnixNano()
			blocker, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Close()
			if _, err := blocker.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
				t.Fatal(err)
			}
			released := false
			release := func() {
				if !released {
					_, _ = blocker.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockID)
					released = true
				}
			}
			defer release()
			// Pause a later INSERT after earlier private batches have committed.
			ddl := fmt.Sprintf(`CREATE FUNCTION block_staging() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.name='%s' THEN PERFORM pg_advisory_xact_lock(%d); END IF; RETURN NEW; END $$; CREATE TRIGGER staging_pause BEFORE INSERT ON replication_configs FOR EACH ROW EXECUTE FUNCTION block_staging()`, record.Key.Name, lockID)
			if _, err := db.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			importCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- edge.ApplyMirror(importCtx, lease, batch, bytes.NewReader(data.Bytes())) }()
			joined := false
			defer func() {
				cancel()
				release()
				if !joined {
					<-done
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var blocked int
				if err := db.QueryRow("SELECT count(*) FROM pg_stat_activity WHERE wait_event = 'advisory' AND query LIKE '%replication_configs%'").Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("staging did not reach injected write delay")
				}
				time.Sleep(10 * time.Millisecond)
			}
			state, _, err := edge.MirrorCurrent(ctx, key)
			if err != nil || state.Versions[1].Content != "old" {
				t.Fatal("private staging changed active view", state, err)
			}
			renewCtx, stop := context.WithTimeout(ctx, 500*time.Millisecond)
			_, ok, err := edge.AcquireMirrorLease(renewCtx, "owner", time.Minute)
			stop()
			if err != nil || !ok {
				t.Fatal("staging blocked lease renewal", err)
			}
			if _, err := edge.Cleanup(ctx, "cleaner", 100, time.Nanosecond, 32); err != nil {
				t.Fatal(err)
			}
			if outcome == "cancel" {
				cancel()
			}
			var successor replication.Lease
			if outcome == "takeover" {
				// Expire the lease while a private batch is still in progress.
				if _, err := db.Exec("UPDATE replication_mirror SET expires_at=0 WHERE id=1"); err != nil {
					t.Fatal(err)
				}
				var ok bool
				successor, ok, err = edge.AcquireMirrorLease(ctx, "successor", time.Minute)
				if err != nil || !ok || successor.Epoch <= lease.Epoch {
					t.Fatal("lease takeover failed", successor, err)
				}
			}
			release()
			err = <-done
			joined = true
			if cancelImport && err == nil {
				t.Fatal("canceled staging activated")
			}
			if !cancelImport && err != nil {
				t.Fatal(err)
			}
			if outcome == "takeover" && !errors.Is(err, replication.ErrLeaseLost) {
				t.Fatal("stale staging was not fenced", err)
			}
			expected, cursor := "new", batch.Cursor
			if cancelImport {
				expected, cursor = "old", base.Cursor
			}
			status, err := edge.MirrorStatus(ctx)
			if err != nil || status.Cursor != cursor {
				t.Fatal(status, err)
			}
			for i := 0; i < 12; i++ {
				if _, err := edge.Cleanup(ctx, "cleaner", 100, time.Nanosecond, 32); err != nil {
					t.Fatal(err)
				}
				state, _, err := edge.MirrorCurrent(ctx, key)
				if err != nil || state.Versions[state.GlobalVersion].Content != expected {
					t.Fatal("cleanup damaged active view", state, err)
				}
			}
			bulk := key
			bulk.Name = "bulk-000"
			_, _, err = edge.MirrorCurrent(ctx, bulk)
			if cancelImport && !errors.Is(err, config.ErrNotFound) {
				t.Fatal("partial snapshot became visible", err)
			}
			if !cancelImport && err != nil {
				t.Fatal("complete snapshot missing key", err)
			}
			if outcome == "takeover" {
				if err := edge.ApplyMirror(ctx, successor, batch, bytes.NewReader(data.Bytes())); err != nil {
					t.Fatal("successor could not replace mirror", err)
				}
				state, _, err := edge.MirrorCurrent(ctx, key)
				if err != nil || state.Versions[state.GlobalVersion].Content != "new" {
					t.Fatal("successor mirror is incomplete", state, err)
				}
			}
		})
	}
}

func TestGenerationResetCursorSurvivesEventCleanup(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "reset"}
	var state *config.State
	var cursor replication.Cursor
	for i := 0; i < 3; i++ {
		edit := config.Edit{Content: fmt.Sprint(i), Format: "text"}
		if state != nil {
			edit.ExpectedID = state.ID
			edit.ExpectedRevision = state.Revision
		}
		saved, err := cloud.Save(ctx, key, edit)
		if err != nil {
			t.Fatal(err)
		}
		state = saved.State
		var data bytes.Buffer
		after := cursor
		if i == 2 {
			after = replication.Cursor{}
		}
		batch, err := cloud.Export(ctx, after, &data)
		if err != nil {
			t.Fatal(err)
		}
		lease, _, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
			t.Fatal(err)
		}
		cursor = batch.Cursor
	}
	_, reset, err := edge.MirrorCurrent(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := edge.Cleanup(ctx, "cleaner", 100, time.Nanosecond, 100); err != nil {
		t.Fatal(err)
	}
	changes, err := edge.Changes(ctx, 0, 100)
	if err != nil || changes.PurgedThrough < reset {
		t.Fatal("cleanup moved reset watermark backwards", changes, err)
	}
}

func TestSchemaSevenMirrorRemainsReadableAndUpgradesOnIncrement(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	guardDown, err := os.ReadFile("migrations/postgres/000010_mirror_rebuild_guard.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(guardDown)); err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("migrations/postgres/000008_mirror_generations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_migrations SET version=7"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	edge, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer edge.Close()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "legacy"}
	saved, err := edge.Save(ctx, key, config.Edit{Content: "offline baseline", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE replication_mirror SET ready=true,source_id='legacy-source',source_sequence=9"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate("postgres", dsn, true); err == nil {
		t.Fatal("implicit schema7 upgrade accepted")
	}
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	edge.DisableManagementWrites()
	current, _, err := edge.MirrorCurrent(ctx, key)
	if err != nil || current.ID != saved.State.ID || current.Versions[1].Content != "offline baseline" {
		t.Fatal(current, err)
	}
	if names, err := edge.MirrorNamespaces(ctx); err != nil || len(names) != 1 || names[0] != "public" {
		t.Fatal(names, err)
	}
	if groups, err := edge.MirrorGroups(ctx, "public"); err != nil || len(groups) != 1 || groups[0] != "DEFAULT_GROUP" {
		t.Fatal(groups, err)
	}
	if rows, err := edge.MirrorList(ctx, "public", "DEFAULT_GROUP", "", 25); err != nil || len(rows) != 1 || rows[0].ID != saved.State.ID || rows[0].GlobalVersion != 1 {
		t.Fatal(rows, err)
	}
	next := *saved.State
	next.Revision++
	next.LastVersion = 2
	next.GlobalVersion = 2
	v := next.Versions[1]
	v.Number = 2
	v.Content = "caught up"
	next.Versions = map[int64]config.Version{2: v}
	var data bytes.Buffer
	if err := replication.NewRecordEncoder(&data).Encode(replication.Record{Key: key, State: &next}); err != nil {
		t.Fatal(err)
	}
	lease, _, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	batch := replication.Manifest{Cursor: replication.Cursor{Source: "legacy-source", Sequence: 10}, After: 9, Count: 1}
	if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
		t.Fatal(err)
	}
	if _, err := edge.Cleanup(ctx, "cleaner", 100, time.Hour, 128); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, _, err = reopened.MirrorCurrent(ctx, key)
	if err != nil || current.ID != saved.State.ID || current.Versions[2].Content != "caught up" {
		t.Fatal(current, err)
	}
}
