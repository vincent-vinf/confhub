package transport_test

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
)

type blackholeWriter struct {
	io.Writer
	drop *atomic.Bool
}

func (w blackholeWriter) Write(p []byte) (int, error) {
	if w.drop.Load() {
		return len(p), nil
	}
	return w.Writer.Write(p)
}

func TestNATSListenerDetectsSilentNetworkOutage(t *testing.T) {
	address := os.Getenv("CONFHUB_TEST_NATS_URL")
	if address == "" {
		t.Skip("set CONFHUB_TEST_NATS_URL for real broker integration")
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
	var drop atomic.Bool
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
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
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			_, _ = io.Copy(blackholeWriter{client, &drop}, upstream)
		}()
		_, _ = io.Copy(blackholeWriter{upstream, &drop}, client)
		upstream.Close()
		<-copied
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	listenerDone := make(chan error, 1)
	accepted := make(chan struct{}, 1)
	proxied := *broker
	proxied.Host = proxy.Addr().String()
	subject := "confhub.outage." + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "")
	token := "replication-test-01234567890123456789"
	go func() {
		listenerDone <- transport.ListenNATS(ctx, proxied.String(), subject, token, func(ctx context.Context, _ replication.Conn) {
			accepted <- struct{}{}
			<-ctx.Done()
		})
	}()
	defer func() { cancel(); <-proxyDone }()
	for {
		dialCtx, stop := context.WithTimeout(ctx, time.Second)
		conn, err := transport.DialNATS(dialCtx, address, subject, token)
		stop()
		if err == nil {
			defer conn.Close()
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-accepted
	drop.Store(true)
	select {
	case err := <-listenerDone:
		if err == nil {
			t.Fatal("silent outage completed without error")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("cloud listener did not release its dead broker connection for reconnect")
	}
}
