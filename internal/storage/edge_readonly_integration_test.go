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

func TestReadOnlyEdgeRejectsManagementWritesAndAcceptsReplication(t *testing.T) {
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx := context.Background()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "readonly"}
	first, err := cloud.Save(ctx, key, config.Edit{Content: "cloud", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	edge.DisableManagementWrites()
	checks := []func() error{
		func() error { _, err := edge.Save(ctx, key, config.Edit{Content: "local", Format: "text"}); return err },
		func() error { _, err := edge.Delete(ctx, key, first.State.ID, first.State.Revision); return err },
		func() error { _, err := edge.CopyVersion(ctx, key, first.State.ID, 1, 1, "global", true); return err },
		func() error { _, err := edge.SetRules(ctx, key, first.State.ID, 1, nil, 0); return err },
		func() error { return edge.CreateNamespace(ctx, "local") },
		func() error { return edge.CreateGroup(ctx, "public", "local") },
		func() error { return edge.DeleteNamespace(ctx, "public") },
		func() error { return edge.InitializeAdmin(ctx, "local-password") },
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, storage.ErrReadOnly) {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
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
	state, _, err := edge.MirrorCurrent(ctx, key)
	if err != nil || state.Versions[1].Content != "cloud" {
		t.Fatal(state, err)
	}
	if _, err := edge.Cleanup(ctx, "cleaner", 100, time.Hour, 100); err != nil {
		t.Fatal(err)
	}
	if err := edge.SyncPresence(ctx, "edge", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	audit := storage.Audit{Action: "auth.login", CreatedAt: time.Now(), RequestID: "edge-local-login"}
	audit.ID, err = edge.BeginAudit(ctx, audit)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.FinishAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	if err := edge.ReleaseMirrorLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
}
