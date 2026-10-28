package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestRawSnapshotCanBeAppliedWithoutManufacturingVersions(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "mirror"}
	saved, err := cloud.Save(ctx, key, config.Edit{Content: "main", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := edge.AcquireMirrorLease(ctx, "edge-a", time.Minute)
	if err != nil || !ok {
		t.Fatal(lease, ok, err)
	}
	if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
		t.Fatal(err)
	}
	state, seq, err := edge.MirrorCurrent(ctx, key)
	if err != nil || state.ID != saved.State.ID || state.GlobalVersion != 1 || state.Versions[1].Content != "main" || seq < 1 {
		t.Fatal(state, seq, err)
	}
	status, err := edge.MirrorStatus(ctx)
	if err != nil || !status.Ready || status.Cursor != batch.Cursor {
		t.Fatal(status, err)
	}
}

func TestMirrorIncrementalDeletionRecreationAndIdempotentReplay(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "identity"}
	m, err := cloud.Save(ctx, k, config.Edit{Content: "old", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	apply := func(after replication.Cursor) replication.Manifest {
		t.Helper()
		var data bytes.Buffer
		batch, err := cloud.Export(ctx, after, &data)
		if err != nil {
			t.Fatal(err)
		}
		raw := append([]byte(nil), data.Bytes()...)
		if err := edge.ApplyMirror(ctx, lease, batch, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		_, before, _ := edge.MirrorCurrent(ctx, k)
		if err := edge.ApplyMirror(ctx, lease, batch, bytes.NewReader(raw)); err != nil {
			t.Fatal("replay", err)
		}
		_, afterSeq, _ := edge.MirrorCurrent(ctx, k)
		if afterSeq != before {
			t.Fatal("replay generated local events")
		}
		return batch
	}
	first := apply(replication.Cursor{})
	if _, err := cloud.Delete(ctx, k, m.State.ID, m.State.Revision); err != nil {
		t.Fatal(err)
	}
	second := apply(first.Cursor)
	if second.Full || second.Count != 1 {
		t.Fatal("deletion was not incremental", second)
	}
	if _, _, err := edge.MirrorCurrent(ctx, k); !errors.Is(err, config.ErrNotFound) {
		t.Fatal(err)
	}
	recreated, err := cloud.Save(ctx, k, config.Edit{Content: "new", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	apply(second.Cursor)
	got, _, err := edge.MirrorCurrent(ctx, k)
	if err != nil || got.ID == m.State.ID || got.ID != recreated.State.ID || got.Versions[1].Content != "new" {
		t.Fatal(got, err)
	}
}

func TestMirrorLeaseFencesFormerOwnerAndRejectsPartialInput(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "fenced"}
	if _, err := cloud.Save(ctx, k, config.Edit{Content: "complete", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	old, ok, err := edge.AcquireMirrorLease(ctx, "old", 10*time.Millisecond)
	if err != nil || !ok {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	next, ok, err := edge.AcquireMirrorLease(ctx, "new", time.Minute)
	if err != nil || !ok || next.Epoch <= old.Epoch {
		t.Fatal(next, ok, err)
	}
	if err := edge.ApplyMirror(ctx, old, batch, bytes.NewReader(data.Bytes())); !errors.Is(err, replication.ErrLeaseLost) {
		t.Fatal("former owner applied", err)
	}
	if err := edge.ApplyMirror(ctx, next, batch, strings.NewReader("")); err == nil {
		t.Fatal("partial transfer applied")
	}
	status, err := edge.MirrorStatus(ctx)
	if err != nil || status.Ready {
		t.Fatal(status, err)
	}
	if err := edge.ApplyMirror(ctx, next, batch, bytes.NewReader(data.Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := edge.ReleaseMirrorLease(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := edge.AcquireMirrorLease(ctx, "third", time.Minute); err != nil || ok {
		t.Fatal("former owner released successor lease", ok, err)
	}
}

func TestFullCompensationClearsDeletedKeysAfterLogExpiry(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "expired"}
	m, err := cloud.Save(ctx, k, config.Edit{Content: "old", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	first, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := edge.AcquireMirrorLease(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.ApplyMirror(ctx, lease, first, &data); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.Delete(ctx, k, m.State.ID, m.State.Revision); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := cloud.Cleanup(ctx, "cleaner", 100, time.Millisecond, 100); err != nil {
		t.Fatal(err)
	}
	data.Reset()
	batch, err := cloud.Export(ctx, first.Cursor, &data)
	if err != nil || !batch.Full || batch.Count != 0 {
		t.Fatal(batch, err)
	}
	if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
		t.Fatal(err)
	}
	if _, _, err := edge.MirrorCurrent(ctx, k); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("ghost survived full replacement", err)
	}
}

func TestConcurrentExportsContainStateFromTheirWatermark(t *testing.T) {
	cloud := testutil.Store(t)
	ctx := context.Background()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "concurrent"}
	first, err := cloud.Save(ctx, k, config.Edit{Content: "version 1", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := cloud.Watermark(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		state := first.State
		for i := 2; i <= 50; i++ {
			m, err := cloud.Save(ctx, k, config.Edit{ExpectedID: state.ID, ExpectedRevision: state.Revision, Content: fmt.Sprintf("version %d", i), Format: "text"})
			if err != nil {
				done <- err
				return
			}
			state = m.State
		}
	}()
	for i := 0; i < 20; i++ {
		var data bytes.Buffer
		batch, err := cloud.Export(ctx, cursor, &data)
		if err != nil {
			t.Fatal(err)
		}
		if batch.Count == 0 {
			continue
		}
		var r replication.Record
		if err := replication.NewRecordDecoder(&data).Decode(&r); err != nil {
			t.Fatal(err)
		}
		if r.State == nil || r.State.GlobalVersion != batch.Sequence || r.State.Versions[batch.Sequence].Content != fmt.Sprintf("version %d", batch.Sequence) {
			t.Fatal("mixed snapshot", batch, r.State)
		}
	}
	wg.Wait()
	select {
	case err := <-done:
		t.Fatal(err)
	default:
	}
}

func TestMirrorRejectsSourceSwitchAndCursorRollback(t *testing.T) {
	cloud, other, edge := testutil.Store(t), testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "source"}
	if _, err := cloud.Save(ctx, key, config.Edit{Content: "keep", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, _ := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
	raw := append([]byte(nil), data.Bytes()...)
	if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
		t.Fatal(err)
	}
	data.Reset()
	foreign, err := other.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.ApplyMirror(ctx, lease, foreign, &data); !errors.Is(err, replication.ErrSourceChanged) {
		t.Fatal(err)
	}
	batch.Sequence = 0
	if err := edge.ApplyMirror(ctx, lease, batch, bytes.NewReader(raw)); !errors.Is(err, replication.ErrSourceChanged) {
		t.Fatal(err)
	}
	if _, err := cloud.Export(ctx, replication.Cursor{Source: foreign.Source}, io.Discard); !errors.Is(err, replication.ErrSourceChanged) {
		t.Fatal(err)
	}
	got, _, err := edge.MirrorCurrent(ctx, key)
	if err != nil || got.Versions[1].Content != "keep" {
		t.Fatal(got, err)
	}
}
