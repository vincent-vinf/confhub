package transport

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/proto"
)

// ValidSubject rejects wildcards, whitespace and empty subject tokens.
func ValidSubject(subject string) bool {
	if subject == "" || len(subject) > 128 || strings.ContainsAny(subject, "*> \t\r\n") {
		return false
	}
	for _, part := range strings.Split(subject, ".") {
		if part == "" {
			return false
		}
	}
	return true
}

type natsConn struct {
	nc       *nats.Conn
	sub      *nats.Subscription
	output   string
	owns     bool
	once     sync.Once
	lifetime context.Context
	cancel   context.CancelFunc
	out      chan []byte
}

func connectNATS(ctx context.Context, address string) (*nats.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	timeout := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	// A silent WAN partition must retire the shared cloud connection too;
	// the broker client's two-minute default would delay new sessions.
	return nats.Connect(address, nats.Timeout(timeout), nats.NoReconnect(), nats.FlusherTimeout(10*time.Second), nats.PingInterval(2*time.Second), nats.MaxPingsOutstanding(2), nats.Name("confhub-replication"))
}
func subscribe(nc *nats.Conn, input, output string, owns bool) (*natsConn, error) {
	sub, err := nc.SubscribeSync(input)
	if err != nil {
		return nil, err
	}
	if err := sub.SetPendingLimits(16, 16*replication.MaxMessageBytes); err != nil {
		sub.Unsubscribe()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	conn := &natsConn{nc: nc, sub: sub, output: output, owns: owns, lifetime: ctx, cancel: cancel, out: make(chan []byte, 8)}
	go conn.write()
	return conn, nil
}

// Send accepts a message into a bounded local queue. NATS batches network
// writes; the protocol window ACK proves receipt without a PING/PONG per frame.
func (n *natsConn) Send(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) > replication.MaxMessageBytes {
		return errors.New("replication message too large")
	}
	if n.lifetime.Err() != nil || !n.nc.IsConnected() || !n.sub.IsValid() {
		return replication.ErrClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-n.lifetime.Done():
		return replication.ErrClosed
	case n.out <- append([]byte(nil), data...):
		return nil
	}
}
func (n *natsConn) write() {
	for {
		select {
		case <-n.lifetime.Done():
			return
		case data := <-n.out:
			if n.lifetime.Err() != nil {
				return
			}
			if err := n.nc.Publish(n.output, data); err != nil {
				n.cancel()
				return
			}
		}
	}
}
func (n *natsConn) Recv(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(n.lifetime, cancel)
	defer stop()
	msg, err := n.sub.NextMsgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(msg.Data) > replication.MaxMessageBytes {
		return nil, errors.New("replication message too large")
	}
	return msg.Data, nil
}
func (n *natsConn) Close() error {
	n.once.Do(func() {
		n.cancel()
		n.sub.Unsubscribe()
		if n.owns {
			n.nc.Close()
		}
	})
	return nil
}

// DialNATS creates a dedicated bidirectional session. Each edge has distinct
// subjects: changes are not load-balanced between independent edge mirrors.
func DialNATS(ctx context.Context, address, subject, token string) (replication.Conn, error) {
	if !ValidSubject(subject) {
		return nil, errors.New("invalid NATS replication subject")
	}
	nc, err := connectNATS(ctx, address)
	if err != nil {
		return nil, err
	}
	session := uuid.NewString()
	prefix := subject + ".session." + session
	conn, err := subscribe(nc, prefix+".down", prefix+".up", true)
	if err != nil {
		nc.Close()
		return nil, err
	}
	if err := nc.FlushWithContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	raw, err := proto.Marshal(&wire.OpenRequest{Version: replication.ProtocolVersion, Session: session, Token: token})
	if err != nil {
		conn.Close()
		return nil, err
	}
	reply, err := nc.RequestWithContext(ctx, subject+".open", raw)
	var response wire.OpenReply
	if err == nil {
		err = proto.Unmarshal(reply.Data, &response)
	}
	if err == nil && (response.Version != replication.ProtocolVersion || !response.Ok) {
		err = errors.New("replication session denied")
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// ListenNATS accepts bounded sessions and runs the same handler as WebSocket.
// The open request is load-balanced among cloud replicas; session traffic is not.
// Broker permissions must restrict session subjects to trusted cloud/edge peers.
func ListenNATS(ctx context.Context, address, subject, token string, handler func(context.Context, replication.Conn)) error {
	if !ValidSubject(subject) || len(token) < 32 {
		return errors.New("invalid NATS replication settings")
	}
	nc, err := connectNATS(ctx, address)
	if err != nil {
		return err
	}
	defer nc.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	closing := false
	var workers sync.WaitGroup
	capacity := make(chan struct{}, 128)
	expected := sha256.Sum256([]byte(token))
	sub, err := nc.QueueSubscribe(subject+".open", "confhub-cloud-sync", func(message *nats.Msg) {
		var req wire.OpenRequest
		respond := func(ok bool) error {
			raw, err := proto.Marshal(&wire.OpenReply{Version: replication.ProtocolVersion, Ok: ok})
			if err != nil {
				return err
			}
			return message.Respond(raw)
		}
		denied := func() { _ = respond(false) }
		if message.Reply == "" || len(message.Data) > 4096 || proto.Unmarshal(message.Data, &req) != nil || req.Version != replication.ProtocolVersion {
			denied()
			return
		}
		provided := sha256.Sum256([]byte(req.Token))
		if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
			denied()
			return
		}
		if parsed, err := uuid.Parse(req.Session); err != nil || parsed.String() != req.Session {
			denied()
			return
		}
		mu.Lock()
		if closing {
			mu.Unlock()
			denied()
			return
		}
		select {
		case capacity <- struct{}{}:
		default:
			mu.Unlock()
			denied()
			return
		}
		workers.Add(1)
		mu.Unlock()
		go func() {
			defer workers.Done()
			defer func() { <-capacity }()
			prefix := subject + ".session." + req.Session
			conn, err := subscribe(nc, prefix+".up", prefix+".down", false)
			if err != nil {
				denied()
				return
			}
			defer conn.Close()
			setupCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err = nc.FlushWithContext(setupCtx)
			stop()
			if err != nil {
				denied()
				return
			}
			if err := respond(true); err != nil {
				return
			}
			handler(ctx, conn)
		}()
	})
	if err != nil {
		return err
	}
	flushCtx, stopFlush := context.WithTimeout(ctx, 5*time.Second)
	err = nc.FlushWithContext(flushCtx)
	stopFlush()
	if err != nil {
		sub.Unsubscribe()
		return err
	}
	for ctx.Err() == nil && nc.IsConnected() {
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	mu.Lock()
	closing = true
	mu.Unlock()
	cancel()
	sub.Unsubscribe()
	workers.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return replication.ErrClosed
}
