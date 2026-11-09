package storage_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestMirrorRebuildGuardIsFencedPersistentAndPreservesReads(t *testing.T) {
	cloud := testutil.Store(t)
	dsn := testutil.Database(t)
	ctx := context.Background()
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	edge, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer edge.Close()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "guarded"}
	if _, err := cloud.Save(ctx, key, config.Edit{Content: "retained", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), data.Bytes()...)
	old, ok, err := edge.AcquireMirrorLease(ctx, "old", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := edge.ReleaseMirrorLease(ctx, old); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := edge.AcquireMirrorLease(ctx, "new", time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := edge.RequireMirrorRebuild(ctx, old); !errors.Is(err, replication.ErrLeaseLost) {
		t.Fatal("stale worker fenced the active mirror", err)
	}
	if err := edge.ApplyMirror(ctx, lease, batch, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	edge.DisableManagementWrites()
	for i := 0; i < 2; i++ {
		if err := edge.RequireMirrorRebuild(ctx, lease); err != nil {
			t.Fatal("rebuild guard must be idempotent and allowed in edge mode", err)
		}
	}
	if err := edge.ApplyMirror(ctx, lease, batch, bytes.NewReader(raw)); !errors.Is(err, replication.ErrSourceChanged) {
		t.Fatal("guarded mirror accepted import", err)
	}
	if err := edge.ReleaseMirrorLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.MirrorStatus(ctx)
	if err != nil || !status.RebuildRequired || !status.Ready || status.Cursor != batch.Cursor {
		t.Fatal("guard was not persisted", status, err)
	}
	if _, _, err := reopened.AcquireMirrorLease(ctx, "replacement", time.Minute); !errors.Is(err, replication.ErrSourceChanged) {
		t.Fatal("replacement worker bypassed rebuild guard", err)
	}
	state, _, err := reopened.MirrorCurrent(ctx, key)
	if err != nil || state.Versions[1].Content != "retained" {
		t.Fatal("guard interrupted local reads", state, err)
	}
	reopened.DisableWrites()
	if err := reopened.RequireMirrorRebuild(ctx, lease); !errors.Is(err, storage.ErrReadOnly) {
		t.Fatal("standalone read-only store accepted guard write", err)
	}
}
