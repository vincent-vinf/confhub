package replication_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/proto"
)

type countedSource struct {
	idleSource
	calls atomic.Int64
}

func (s *countedSource) Watermark(ctx context.Context) (replication.Cursor, error) {
	s.calls.Add(1)
	return s.idleSource.Watermark(ctx)
}

func TestPublisherSharesPollingAcrossSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &countedSource{}
	publisher := replication.NewPublisher(source, 10*time.Millisecond)
	stopped := make(chan struct{})
	go func() { defer close(stopped); publisher.Run(ctx) }()
	defer func() { cancel(); <-stopped }()
	for i := 0; i < 32; i++ {
		a, b := replication.Pipe()
		go func() { _ = publisher.Serve(ctx, a) }()
		defer b.Close()
		if _, err := b.Recv(ctx); err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				raw, err := b.Recv(ctx)
				if err != nil {
					return
				}
				var f wire.Frame
				if proto.Unmarshal(raw, &f) != nil {
					return
				}
				reply, _ := proto.Marshal(&wire.Frame{Version: replication.ProtocolVersion, Type: wire.FrameType_FRAME_TYPE_SEEN})
				_ = b.Send(ctx, reply)
			}
		}()
	}
	source.sequence.Store(7)
	time.Sleep(250 * time.Millisecond)
	if got := source.calls.Load(); got > 50 {
		t.Fatalf("sessions multiplied source polling: %d calls", got)
	}
	cursor, err := publisher.Watermark(ctx)
	if err != nil || cursor.Sequence != 7 {
		t.Fatal(cursor, err)
	}
}
