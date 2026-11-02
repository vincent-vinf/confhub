package replication_test

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

type restoredSource struct {
	replication.Source
	rolledBack atomic.Bool
}

func (s *restoredSource) Watermark(ctx context.Context) (replication.Cursor, error) {
	cursor, err := s.Source.Watermark(ctx)
	if s.rolledBack.Load() {
		cursor.Sequence = 0
	}
	return cursor, err
}

func TestObservedSourceRollbackRequiresRebuildAcrossWorkerRestart(t *testing.T) {
	cloud := testutil.Store(t)
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	edge, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer edge.Close()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "stable"}
	first, err := cloud.Save(ctx, key, config.Edit{Content: "old mirror", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	initial, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := edge.AcquireMirrorLease(ctx, "initial", time.Minute)
	if err != nil || !ok {
		t.Fatal("initial lease", err)
	}
	if err := edge.ApplyMirror(ctx, lease, initial, &data); err != nil {
		t.Fatal(err)
	}
	if err := edge.ReleaseMirrorLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	source := &restoredSource{Source: cloud}
	source.rolledBack.Store(true)
	dial := func(sessionCtx context.Context) (replication.Conn, error) {
		a, b := replication.Pipe()
		go func() { _ = serve(ctx, a, source, 10*time.Millisecond) }()
		return b, nil
	}
	start := func(store *storage.Store) (*replication.Worker, func()) {
		workerCtx, stop := context.WithCancel(ctx)
		worker := replication.NewWorker(store, dial, 3*time.Second)
		done := make(chan struct{})
		go func() { defer close(done); worker.Run(workerCtx) }()
		return worker, func() { stop(); <-done }
	}
	worker, stop := start(edge)
	defer func() { stop() }()
	for worker.Status().Error != "source_changed_rebuild_required" {
		select {
		case <-ctx.Done():
			t.Fatal("rollback was not detected")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := cloud.Save(ctx, key, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Content: "restored source ahead again", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	source.rolledBack.Store(false)
	assertBlocked := func(store *storage.Store) {
		t.Helper()
		until := time.NewTimer(750 * time.Millisecond)
		defer until.Stop()
		for {
			status, err := store.MirrorStatus(ctx)
			if err != nil || status.Cursor != initial.Cursor || !status.Ready {
				t.Fatalf("observed rollback resumed without rebuilding: %+v %v", status, err)
			}
			state, _, err := store.MirrorCurrent(ctx, key)
			if err != nil || state.Versions[1].Content != "old mirror" {
				t.Fatal("blocked mirror no longer serves old content", state, err)
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-until.C:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	assertBlocked(edge)
	stop()
	stop = func() {}
	if err := edge.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	worker, stop = start(reopened)
	assertBlocked(reopened)
	if worker.Status().Error != "source_changed_rebuild_required" {
		t.Fatal("restarted worker did not expose persistent rebuild requirement", worker.Status())
	}
}
