package transport_test

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
)

type delayedWriter struct{ io.Writer }

func (w delayedWriter) Write(data []byte) (int, error) {
	time.Sleep(100 * time.Millisecond)
	return w.Writer.Write(data)
}

func TestNATSSendsWindowWithoutPerMessageBrokerRoundTrips(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("requires test broker")
	}
	broker, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		client, err := proxy.Accept()
		if err != nil {
			return
		}
		defer client.Close()
		upstream, err := net.DialTimeout("tcp", broker.Host, time.Second)
		if err != nil {
			return
		}
		defer upstream.Close()
		done := make(chan struct{})
		go func() { defer close(done); _, _ = io.Copy(delayedWriter{client}, upstream) }()
		_, _ = io.Copy(upstream, client)
		upstream.Close()
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subject, token := "confhub.latency."+uuid.NewString(), "replication-test-01234567890123456789"
	listenerDone := make(chan error, 1)
	received := make(chan struct{}, 8)
	go func() {
		listenerDone <- transport.ListenNATS(ctx, address, subject, token, func(ctx context.Context, c replication.Conn) {
			for i := 0; i < 8; i++ {
				if _, err := c.Recv(ctx); err != nil {
					return
				}
				received <- struct{}{}
			}
		})
	}()
	defer func() { cancel(); <-listenerDone }()
	proxied := *broker
	proxied.Host = proxy.Addr().String()
	var conn replication.Conn
	for {
		dialCtx, stop := context.WithTimeout(ctx, time.Second)
		conn, err = transport.DialNATS(dialCtx, proxied.String(), subject, token)
		stop()
		if err == nil {
			break
		}
		t.Fatal(err)
	}
	defer func() { conn.Close(); <-copied }()
	started := time.Now()
	for i := 0; i < 8; i++ {
		if err := conn.Send(ctx, []byte("chunk")); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(started)
	for i := 0; i < 8; i++ {
		select {
		case <-received:
		case <-ctx.Done():
			t.Fatal("window did not arrive")
		}
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("eight sends waited for broker round trips: %s", elapsed)
	}
}
