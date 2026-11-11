package replication_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"

	"bufio"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestInterruptedCorruptAndReorderedTransfersKeepCommittedMirror(t *testing.T) {
	for _, kind := range []string{"missing-end", "wrong-index", "wrong-digest", "wrong-request", "truncated-record", "legacy-json", "unsupported-version", "oversized-record", "invalid-timestamp"} {
		t.Run(kind, func(t *testing.T) {
			cloud, edge := testutil.Store(t), testutil.Store(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "stable"}
			first, err := cloud.Save(ctx, key, config.Edit{Content: "committed", Format: "text"})
			if err != nil {
				t.Fatal(err)
			}
			var data bytes.Buffer
			initial, err := cloud.Export(ctx, replication.Cursor{}, &data)
			if err != nil {
				t.Fatal(err)
			}
			lease, _, err := edge.AcquireMirrorLease(ctx, "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := edge.ApplyMirror(ctx, lease, initial, &data); err != nil {
				t.Fatal(err)
			}
			_, err = cloud.Save(ctx, key, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Content: "new", Format: "text"})
			if err != nil {
				t.Fatal(err)
			}
			data.Reset()
			batch, err := cloud.Export(ctx, initial.Cursor, &data)
			if err != nil {
				t.Fatal(err)
			}
			a, b := replication.Pipe()
			defer a.Close()
			defer b.Close()
			done := make(chan error, 1)
			attackerDone := make(chan struct{})
			go func() {
				defer close(attackerDone)
				defer a.Close()
				send := func(f *wire.Frame) bool {
					f.Version = replication.ProtocolVersion
					raw, _ := proto.Marshal(f)
					return a.Send(ctx, raw) == nil
				}
				if kind == "legacy-json" {
					_ = a.Send(ctx, []byte(`{"version":1,"type":"watermark"}`))
					return
				}
				if kind == "unsupported-version" {
					raw, _ := proto.Marshal(&wire.Frame{Version: replication.ProtocolVersion + 1, Type: wire.FrameType_FRAME_TYPE_WATERMARK})
					_ = a.Send(ctx, raw)
					return
				}
				if !send(&wire.Frame{Type: wire.FrameType_FRAME_TYPE_WATERMARK, Cursor: &wire.Cursor{Source: batch.Source, Sequence: batch.Sequence}}) {
					return
				}
				var request wire.Frame
				for {
					raw, err := a.Recv(ctx)
					if err != nil {
						return
					}
					if proto.Unmarshal(raw, &request) != nil {
						return
					}
					if request.Type == wire.FrameType_FRAME_TYPE_FETCH {
						break
					}
				}
				id := request.RequestId
				if kind == "wrong-request" {
					id = "wrong"
				}
				if !send(&wire.Frame{Type: wire.FrameType_FRAME_TYPE_BEGIN, RequestId: id, Manifest: &wire.Manifest{Cursor: &wire.Cursor{Source: batch.Source, Sequence: batch.Sequence}, After: batch.After, Full: batch.Full, Count: int64(batch.Count)}}) {
					return
				}
				content := data.Bytes()
				if kind == "truncated-record" {
					content = []byte{0x05, 0x0a}
				}
				if kind == "oversized-record" {
					content = binary.AppendUvarint(nil, replication.MaxRecordBytes+1)
				}
				if kind == "invalid-timestamp" {
					var record wire.Record
					if err := protodelim.UnmarshalFrom(bufio.NewReader(bytes.NewReader(content)), &record); err != nil {
						return
					}
					record.State.Versions[record.State.GlobalVersion].CreatedAt = &timestamppb.Timestamp{Seconds: 253402300800}
					var invalid bytes.Buffer
					if _, err := protodelim.MarshalTo(&invalid, &record); err != nil {
						return
					}
					content = invalid.Bytes()
				}
				index := 0
				if kind == "wrong-index" {
					index = 1
				}
				if !send(&wire.Frame{Type: wire.FrameType_FRAME_TYPE_CHUNK, RequestId: id, Index: int64(index), Data: content}) {
					return
				}
				if kind == "missing-end" {
					return
				}
				sum := sha256.Sum256(content)
				digest := sum[:]
				if kind == "wrong-digest" {
					digest = []byte("wrong")
				}
				send(&wire.Frame{Type: wire.FrameType_FRAME_TYPE_END, RequestId: id, Index: 1, Bytes: int64(len(content)), Digest: digest})
			}()
			go func() { done <- replication.Follow(ctx, b, edge, lease, nil) }()
			if err := <-done; err == nil {
				t.Fatal("invalid transfer accepted")
			}
			<-attackerDone
			status, err := edge.MirrorStatus(ctx)
			if err != nil || status.Cursor != initial.Cursor {
				t.Fatal("cursor advanced", status, err)
			}
			state, _, err := edge.MirrorCurrent(ctx, key)
			if err != nil || state.Versions[1].Content != "committed" {
				t.Fatal("partial content exposed", state, err)
			}
			a, b = replication.Pipe()
			defer a.Close()
			defer b.Close()
			go func() { _ = serve(ctx, a, cloud, time.Millisecond) }()
			go func() { done <- replication.Follow(ctx, b, edge, lease, nil) }()
			for {
				state, _, err := edge.MirrorCurrent(ctx, key)
				if err == nil && state.GlobalVersion == 2 && state.Versions[2].Content == "new" {
					break
				}
				select {
				case err := <-done:
					t.Fatal(err)
				case <-ctx.Done():
					t.Fatal("reconnect did not recover")
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
}

func TestSlowFencedImportDoesNotCancelItsOwnLeaseRenewal(t *testing.T) {
	cloud := testutil.Store(t)
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	edge, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer edge.Close()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "slow"}
	if _, err := cloud.Save(ctx, key, config.Edit{Content: "complete", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// External fault injection delays private snapshot writes while lease renewal
	// must remain independent of the blocked staging transaction.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err := blocker.ExecContext(ctx, "LOCK TABLE replication_configs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	var sessions atomic.Int64
	worker := replication.NewWorker(edge, func(dialCtx context.Context) (replication.Conn, error) {
		a, b := replication.Pipe()
		sessions.Add(1)
		go func() { _ = serve(ctx, a, cloud, time.Millisecond) }()
		return b, nil
	}, 3*time.Second, 250*time.Millisecond)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(8 * time.Second)
	if sessions.Load() != 1 {
		t.Fatal("slow import was canceled and restarted", sessions.Load())
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	for {
		state, _, err := edge.MirrorCurrent(ctx, key)
		if err == nil && state.Versions[1].Content == "complete" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("slow import did not complete")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestImportCancellationRollsBackStateAndCursor(t *testing.T) {
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
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "cancel"}
	first, err := cloud.Save(context.Background(), key, config.Edit{Content: "old", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	initial, err := cloud.Export(context.Background(), replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, _ := edge.AcquireMirrorLease(context.Background(), "owner", time.Minute)
	if err := edge.ApplyMirror(context.Background(), lease, initial, &data); err != nil {
		t.Fatal(err)
	}
	if _, err := cloud.Save(context.Background(), key, config.Edit{ExpectedID: first.State.ID, ExpectedRevision: first.State.Revision, Content: "new", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	data.Reset()
	batch, err := cloud.Export(context.Background(), initial.Cursor, &data)
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
	// The active-generation update waits for this external write lock.
	if _, err := blocker.Exec("LOCK TABLE replication_configs IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := edge.ApplyMirror(ctx, lease, batch, &data); err == nil {
		t.Fatal("canceled import succeeded")
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	status, err := edge.MirrorStatus(context.Background())
	if err != nil || status.Cursor != initial.Cursor {
		t.Fatal(status, err)
	}
	current, _, err := edge.MirrorCurrent(context.Background(), key)
	if err != nil || current.GlobalVersion != 1 || current.Versions[1].Content != "old" {
		t.Fatal(current, err)
	}
}
