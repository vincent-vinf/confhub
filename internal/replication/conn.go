// Package replication synchronizes raw configuration state over a replaceable
// message transport. Broker delivery never substitutes for database commit.
package replication

import (
	"context"
	"errors"
	"sync"
)

const MaxMessageBytes = 128 << 10

var ErrClosed = errors.New("replication connection closed")

// Conn carries complete messages. One Send and one Recv may run concurrently.
// Send does not acknowledge remote application. Close is idempotent and unblocks
// both operations; implementations must honor context cancellation and bound queues.
type Conn interface {
	Send(context.Context, []byte) error
	Recv(context.Context) ([]byte, error)
	Close() error
}

type pipe struct {
	in, out chan []byte
	done    chan struct{}
	once    *sync.Once
}

// Pipe provides a bounded in-memory transport for protocol tests and embedding.
func Pipe() (Conn, Conn) {
	a, b := make(chan []byte), make(chan []byte)
	done, once := make(chan struct{}), &sync.Once{}
	return &pipe{a, b, done, once}, &pipe{b, a, done, once}
}

func (p *pipe) Send(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > MaxMessageBytes {
		return errors.New("replication message too large")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return ErrClosed
	case p.out <- append([]byte(nil), data...):
		return nil
	}
}
func (p *pipe) Recv(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return nil, ErrClosed
	case data := <-p.in:
		return data, nil
	}
}
func (p *pipe) Close() error { p.once.Do(func() { close(p.done) }); return nil }
