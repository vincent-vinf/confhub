package replication_test

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/proto"
)

type idleSource struct{ sequence atomic.Int64 }

func (s *idleSource) Watermark(context.Context) (replication.Cursor, error) {
	return replication.Cursor{Source: "source", Sequence: s.sequence.Load()}, nil
}
func (*idleSource) Export(context.Context, replication.Cursor, io.Writer) (replication.Manifest, error) {
	return replication.Manifest{Cursor: replication.Cursor{Source: "source", Sequence: 7}}, nil
}

func TestHeartbeatsReadCurrentWatermarkIndependentlyOfConfiguredPolling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, b := replication.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	source := &idleSource{}
	publisher := replication.NewPublisher(source, 30*time.Second)
	go publisher.Run(ctx)
	go func() { done <- publisher.Serve(ctx, a) }()
	defer func() { cancel(); b.Close(); <-done }()
	for i := 0; i < 3; i++ {
		raw, err := b.Recv(ctx)
		if err != nil {
			t.Fatal("heartbeat missed during low-frequency polling", err)
		}
		var frame wire.Frame
		if err := proto.Unmarshal(raw, &frame); err != nil || frame.GetVersion() != replication.ProtocolVersion || frame.GetType() != wire.FrameType_FRAME_TYPE_WATERMARK {
			t.Fatal(string(raw), err)
		}
		if i == 2 && frame.GetCursor().GetSequence() != 7 {
			t.Fatal("heartbeat advertised a stale cursor after source advanced", string(raw))
		}
		if i == 1 {
			source.sequence.Store(7)
			if _, err := publisher.Export(ctx, replication.Cursor{}, io.Discard); err != nil {
				t.Fatal(err)
			}
		}
		response, err := proto.Marshal(&wire.Frame{Version: replication.ProtocolVersion, Type: wire.FrameType_FRAME_TYPE_SEEN})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Send(ctx, response); err != nil {
			t.Fatal(err)
		}
	}
}
