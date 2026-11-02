package storage_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
	"google.golang.org/protobuf/encoding/protodelim"
)

func TestExportUsesProtobufAndPreservesConfigurationText(t *testing.T) {
	cloud := testutil.Store(t)
	ctx := context.Background()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "protobuf"}
	content := "中文: \"quotes\"\r\n\tpath: C:\\app\\config\n" + strings.Repeat("value\n", 1000)
	saved, err := cloud.Save(ctx, key, config.Edit{Content: content, Format: "text", Description: "描述"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil || batch.Count != 1 {
		t.Fatal(batch, err)
	}
	reader := bufio.NewReader(&data)
	var record wire.Record
	if err := (protodelim.UnmarshalOptions{MaxSize: replication.MaxRecordBytes}).UnmarshalFrom(reader, &record); err != nil {
		t.Fatal("export is not a protobuf stream", err)
	}
	version := record.GetState().GetVersions()[1]
	if record.GetKey().GetName() != key.Name || record.GetState().GetId() != saved.State.ID || version.GetContent() != content || version.GetDescription() != "描述" {
		t.Fatal("export changed configuration fields", &record)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Fatal("unexpected trailing data", err)
	}
}

func TestSchemaEightJSONMirrorRemainsReadableOfflineAndAcceptsProtobuf(t *testing.T) {
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
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "upgrade"}
	m, err := cloud.Save(ctx, k, config.Edit{Content: "offline main", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = cloud.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "gray", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err = cloud.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Content: "offline beta ☁\n\t", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	initial, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.ApplyMirror(ctx, lease, initial, &data); err != nil {
		t.Fatal(err)
	}
	// Prepare an actual schema-8 JSON snapshot, then upgrade without contacting
	// the cloud. SQL here constructs the old fixture; assertions use Store APIs.
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	down, err := os.ReadFile("migrations/postgres/000009_mirror_protobuf.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(down)); err == nil {
		t.Fatal("downgrade accepted a mirror containing protobuf data")
	}
	current, _, err := edge.MirrorCurrent(ctx, k)
	if err != nil || current == nil || current.Beta == nil || current.Versions[1].Content != "offline main" || current.Beta.Content != "offline beta ☁\n\t" {
		t.Fatal("refused downgrade damaged the mirror", current, err)
	}
	legacy, err := json.Marshal(m.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE replication_configs SET state_json=$1,state_proto=NULL", string(legacy)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	guardDown, err := os.ReadFile("migrations/postgres/000010_mirror_rebuild_guard.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(guardDown)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_migrations SET version=8"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate("postgres", dsn, true); err == nil {
		t.Fatal("implicit schema-8 upgrade accepted")
	}
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	edge.DisableManagementWrites()
	current, _, err = edge.MirrorCurrent(ctx, k)
	if err != nil || current == nil || current.Beta == nil || current.ID != m.State.ID || current.Versions[1].Content != "offline main" || current.Beta.Content != "offline beta ☁\n\t" || len(current.Rules) != 1 {
		t.Fatal("offline JSON mirror changed after upgrade", current, err)
	}
	status, err := edge.MirrorStatus(ctx)
	if err != nil || status.Cursor != initial.Cursor || !status.Ready {
		t.Fatal(status, err)
	}
	m, err = cloud.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "protobuf main", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	data.Reset()
	batch, err := cloud.Export(ctx, initial.Cursor, &data)
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.ApplyMirror(ctx, lease, batch, &data); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, _, err = reopened.MirrorCurrent(ctx, k)
	if err != nil || current == nil || current.Beta == nil || current.ID != m.State.ID || current.GlobalVersion != 2 || current.Versions[2].Content != "protobuf main" || current.Beta.Content != "offline beta ☁\n\t" || len(current.Rules) != 1 {
		t.Fatal("protobuf mirror changed after restart", current, err)
	}
}
