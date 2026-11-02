package transport_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestNATSHandshakeRetriesAfterUnresponsiveQueueMember(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("set CONFHUB_TEST_NATS_URL for real broker integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	subject, token := "confhub.retry."+uuid.NewString(), "replication-test-01234567890123456789"
	stale, err := nats.Connect(address)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Close()
	first := make(chan struct{}, 1)
	// The first request reaches a queue member which never replies. The healthy
	// listener starts afterwards, as when a cloud replica reconnects after a partition.
	sub, err := stale.QueueSubscribe(subject+".open", "confhub-cloud-sync", func(_ *nats.Msg) { first <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.FlushWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	dialDone := make(chan error, 1)
	go func() {
		conn, err := transport.DialNATS(ctx, address, subject, token)
		if conn != nil {
			conn.Close()
		}
		dialDone <- err
	}()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("first handshake did not reach the unresponsive queue member")
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	if err := stale.FlushWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- transport.ListenNATS(ctx, address, subject, token, func(ctx context.Context, _ replication.Conn) { <-ctx.Done() })
	}()
	defer func() { cancel(); <-listenerDone }()
	if err := <-dialDone; err != nil {
		t.Fatal("handshake did not retry a request lost to an unresponsive queue member:", err)
	}
}

type slowReceive struct{ replication.Conn }

func (s slowReceive) Recv(ctx context.Context) ([]byte, error) {
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	return s.Conn.Recv(ctx)
}

func TestNATSMultiMegabyteSnapshotHasReceiverBackpressure(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("set CONFHUB_TEST_NATS_URL for real broker integration")
	}
	cloud, edge := testutil.Store(t), testutil.Store(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "slow"}
	content := strings.Repeat("text", 128*1024)
	for i := 0; i < 8; i++ {
		k := key
		k.Name += string(rune('a' + i))
		if _, err := cloud.Save(ctx, k, config.Edit{Content: content, Format: "text"}); err != nil {
			t.Fatal(err)
		}
	}
	subject := "confhub.test." + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	token := "replication-test-01234567890123456789"
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- transport.ListenNATS(ctx, address, subject, token, func(ctx context.Context, c replication.Conn) { _ = serve(ctx, c, cloud, time.Millisecond) })
	}()
	defer func() { cancel(); <-listenerDone }()
	var conn replication.Conn
	for {
		dialCtx, stop := context.WithTimeout(ctx, time.Second)
		c, err := transport.DialNATS(dialCtx, address, subject, token)
		stop()
		if err == nil {
			conn = c
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer conn.Close()
	badCtx, stop := context.WithTimeout(ctx, time.Second)
	bad, err := transport.DialNATS(badCtx, address, subject, "incorrect")
	stop()
	if bad != nil {
		bad.Close()
	}
	if err == nil {
		t.Fatal("wrong token accepted")
	}
	lease, _, err := edge.AcquireMirrorLease(ctx, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	followDone := make(chan error, 1)
	go func() { followDone <- replication.Follow(ctx, slowReceive{conn}, edge, lease, nil) }()
	for {
		status, err := edge.MirrorStatus(ctx)
		if err == nil && status.Ready {
			break
		}
		select {
		case err := <-followDone:
			t.Fatal("slow receiver failed", err)
		case <-ctx.Done():
			t.Fatal("slow snapshot timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	for i := 0; i < 8; i++ {
		k := key
		k.Name += string(rune('a' + i))
		state, _, err := edge.MirrorCurrent(ctx, k)
		if err != nil || state.Versions[1].Content != content {
			t.Fatal("snapshot incomplete", err)
		}
	}
	cancel()
	<-followDone
}

func TestNATSDisconnectedPeerSessionExpires(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("set CONFHUB_TEST_NATS_URL for real broker integration")
	}
	cloud := testutil.Store(t)
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	token := "replication-test-01234567890123456789"
	subject := "confhub.expire." + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	expired := make(chan error, 1)
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- transport.ListenNATS(ctx, address, subject, token, func(ctx context.Context, c replication.Conn) {
			expired <- serve(ctx, c, cloud, 50*time.Millisecond)
		})
	}()
	defer func() { cancel(); <-listenerDone }()
	for {
		dialCtx, stop := context.WithTimeout(ctx, time.Second)
		conn, err := transport.DialNATS(dialCtx, address, subject, token)
		stop()
		if err == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case err := <-expired:
		if err == nil {
			t.Fatal("disconnected session completed without error")
		}
	case <-ctx.Done():
		t.Fatal("orphaned broker session leaked")
	}
}
