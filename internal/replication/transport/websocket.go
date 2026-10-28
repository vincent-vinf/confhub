// Package transport implements the replication message interface. Protocol and
// database progress belong to the caller, not to transport delivery semantics.
package transport

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vincent-vinf/confhub/internal/replication"
)

type ws struct {
	conn *websocket.Conn
	once sync.Once
}

func WebSocket(conn *websocket.Conn) replication.Conn {
	conn.SetReadLimit(replication.MaxMessageBytes)
	return &ws{conn: conn}
}
func DialWebSocket(ctx context.Context, address, token string) (replication.Conn, error) {
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	conn, response, err := websocket.DefaultDialer.DialContext(ctx, address, headers)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return WebSocket(conn), nil
}
func (w *ws) Send(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > replication.MaxMessageBytes {
		return errors.New("replication message too large")
	}
	stop := context.AfterFunc(ctx, func() { w.Close() })
	defer stop()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	if err := w.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	err := w.conn.WriteMessage(websocket.BinaryMessage, data)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func (w *ws) Recv(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { w.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := w.conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	kind, data, err := w.conn.ReadMessage()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if kind != websocket.BinaryMessage {
		return nil, errors.New("replication requires binary messages")
	}
	return data, nil
}
func (w *ws) Close() error { w.once.Do(func() { w.conn.Close() }); return nil }
