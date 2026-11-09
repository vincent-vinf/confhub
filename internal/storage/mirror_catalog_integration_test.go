package storage_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestMirrorCatalogPagesOnlyActiveSnapshotAndSurvivesFailedReplacement(t *testing.T) {
	ctx := context.Background()
	cloud, edge := testutil.Store(t), testutil.Store(t)
	if _, err := edge.MirrorNamespaces(ctx); !errors.Is(err, replication.ErrUninitialized) {
		t.Fatal(err)
	}
	if err := cloud.CreateNamespace(ctx, "region"); err != nil {
		t.Fatal(err)
	}
	if err := cloud.CreateGroup(ctx, "region", "apps"); err != nil {
		t.Fatal(err)
	}
	keys := []config.Key{{Namespace: "region", Group: "apps", Name: "a"}, {Namespace: "region", Group: "apps", Name: "b"}}
	for _, key := range keys {
		if _, err := cloud.Save(ctx, key, config.Edit{Content: key.Name, Format: "text"}); err != nil {
			t.Fatal(err)
		}
	}
	lease, ok, err := edge.AcquireMirrorLease(ctx, "catalog", time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	apply := func() {
		t.Helper()
		var data bytes.Buffer
		batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
		if err != nil {
			t.Fatal(err)
		}
		if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	if got, err := edge.MirrorNamespaces(ctx); err != nil || !reflect.DeepEqual(got, []string{"region"}) {
		t.Fatal(got, err)
	}
	if got, err := edge.MirrorGroups(ctx, "region"); err != nil || !reflect.DeepEqual(got, []string{"apps"}) {
		t.Fatal(got, err)
	}
	first, err := edge.MirrorList(ctx, "region", "apps", "", 1)
	if err != nil || len(first) != 1 || first[0].Key.Name != "a" || first[0].GlobalVersion != 1 || first[0].PublishedAt.IsZero() {
		t.Fatal(first, err)
	}
	second, err := edge.MirrorList(ctx, "region", "apps", "a", 1)
	if err != nil || len(second) != 1 || second[0].Key.Name != "b" {
		t.Fatal(second, err)
	}
	if _, err := edge.MirrorList(ctx, "region", "apps", "", 201); !errors.Is(err, config.ErrInvalid) {
		t.Fatal(err)
	}
	for _, key := range keys {
		state, err := cloud.Snapshot(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cloud.Delete(ctx, key, state.ID, state.Revision); err != nil {
			t.Fatal(err)
		}
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.ApplyMirror(ctx, lease, batch, bytes.NewBufferString("invalid")); err == nil {
		t.Fatal("bad replacement accepted")
	}
	if got, err := edge.MirrorList(ctx, "region", "apps", "", 100); err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	apply()
	if got, err := edge.MirrorNamespaces(ctx); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if got, err := edge.MirrorList(ctx, "region", "apps", "", 100); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
