package transport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
)

func TestWebSocketCommunicationAndCanceledRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := transport.WebSocket(raw)
		defer conn.Close()
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		data, err := conn.Recv(ctx)
		if err == nil {
			_ = conn.Send(ctx, data)
		}
		_, _ = conn.Recv(ctx)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialWebSocket(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "token")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Send(ctx, []byte("message")); err != nil {
		t.Fatal(err)
	}
	data, err := conn.Recv(ctx)
	if err != nil || string(data) != "message" {
		t.Fatal(string(data), err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := conn.Recv(canceled); err == nil {
		t.Fatal("cancellation ignored")
	}
}
