package confhub

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Callback func(context.Context, Snapshot)

// SubscribeOptions controls initial notification and duplicate registration.
// Zero values preserve Subscribe's behavior.
type SubscribeOptions struct {
	// SkipInitial suppresses the initial state, while keeping changes that
	// arrive between the initial HTTP read and WebSocket registration.
	SkipInitial bool
	// RequireExisting rejects a missing configuration before replacing a watch.
	RequireExisting bool
	// Replace atomically replaces an existing subscription after a successful
	// initial read. An already running old callback may finish.
	Replace bool
}

type subscription struct {
	ctx     context.Context
	cancel  context.CancelFunc
	pending chan Snapshot
	last    *Snapshot
}

// Subscribe registers one callback per key without a fixed subscription count limit. The
// initial snapshot may be offline; a missing key delivers a deletion snapshot.
// Callbacks run independently of network receive and must honor their context.
func (c *Client) Subscribe(ctx context.Context, key Key, callback Callback) error {
	_, err := c.SubscribeWithOptions(ctx, key, SubscribeOptions{}, callback)
	return err
}

// SubscribeWithOptions reads the current state once and registers a callback,
// returning that initial state. Later notifications may start before it returns.
// A failed registration leaves an existing subscription in place.
func (c *Client) SubscribeWithOptions(ctx context.Context, key Key, options SubscribeOptions, callback Callback) (Snapshot, error) {
	key, err := key.normalized()
	if err != nil {
		return Snapshot{}, err
	}
	if callback == nil {
		return Snapshot{}, fmt.Errorf("confhub: callback is required")
	}
	value, err := c.Get(ctx, key)
	if err != nil && (!errors.Is(err, ErrNotFound) || options.RequireExisting) {
		return value, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ctx.Err() != nil {
		return Snapshot{}, ErrClosed
	}
	if ctx.Err() != nil {
		return Snapshot{}, ctx.Err()
	}
	// A current watch may have received a newer state during the HTTP read.
	value = c.records[key]
	if options.RequireExisting && value.Deleted {
		return value, ErrNotFound
	}
	if old := c.subscriptions[key]; old != nil {
		if !options.Replace {
			return Snapshot{}, fmt.Errorf("confhub: key already subscribed")
		}
		old.cancel()
	}
	lifetime, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	sub := &subscription{ctx: lifetime, cancel: cancel, pending: make(chan Snapshot, 1)}
	c.subscriptions[key] = sub
	if options.SkipInitial {
		initial := value
		sub.last = &initial
	} else {
		c.enqueueLocked(sub, value)
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer stop()
		defer cancel()
		defer func() {
			c.mu.Lock()
			if c.subscriptions[key] == sub {
				delete(c.subscriptions, key)
				c.resetLocked()
			}
			c.mu.Unlock()
		}()
		for {
			select {
			case <-sub.ctx.Done():
				return
			case next := <-sub.pending:
				if sub.ctx.Err() != nil || c.ctx.Err() != nil {
					return
				}
				func() {
					defer func() {
						if recover() != nil {
							c.report(fmt.Errorf("confhub: subscription callback panicked"))
						}
					}()
					callback(sub.ctx, next)
				}()
			}
		}
	}()
	c.resetLocked()
	return value, nil
}

// Unsubscribe cancels pending callbacks and releases the subscription. An
// already running callback stops cooperatively through its supplied context.
func (c *Client) Unsubscribe(key Key) error {
	key, err := key.normalized()
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ctx.Err() != nil {
		return ErrClosed
	}
	if sub := c.subscriptions[key]; sub != nil {
		delete(c.subscriptions, key)
		sub.cancel()
		c.resetLocked()
	}
	return nil
}

func (c *Client) enqueueLocked(sub *subscription, value Snapshot) {
	if sub.last != nil && sub.last.ID == value.ID && sub.last.Version == value.Version && sub.last.Beta == value.Beta && sub.last.Deleted == value.Deleted && sub.last.RuleID == value.RuleID && sub.last.Content == value.Content && sub.last.Format == value.Format {
		return
	}
	copy := value
	sub.last = &copy
	select {
	case sub.pending <- value:
	default:
		select {
		case <-sub.pending:
		default:
		}
		sub.pending <- value
	}
}

// Subscription changes rebuild the single multiplexed connection. The
// generation fence discards a dial or message from an obsolete connection.
func (c *Client) resetLocked() {
	c.generation++
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) watch() {
	backoff := 500 * time.Millisecond
	addressIndex := 0
	for c.ctx.Err() == nil {
		c.mu.Lock()
		generation := c.generation
		keys := make([]Key, 0, len(c.subscriptions))
		for key := range c.subscriptions {
			keys = append(keys, key)
		}
		select {
		case <-c.wake:
		default:
		}
		c.mu.Unlock()
		if len(keys) == 0 {
			select {
			case <-c.ctx.Done():
				return
			case <-c.wake:
				continue
			}
		}
		for attempt := 0; attempt < len(c.addresses) && c.ctx.Err() == nil; attempt++ {
			address := c.addresses[addressIndex%len(c.addresses)]
			addressIndex++
			endpoint := c.endpoint(address, "/api/client/watch", nil)
			endpoint = "ws" + strings.TrimPrefix(endpoint, "http")
			dialer := websocket.Dialer{HandshakeTimeout: c.timeout}
			conn, response, err := dialer.DialContext(c.ctx, endpoint, nil)
			if response != nil && response.Body != nil {
				response.Body.Close()
			}
			if err != nil {
				continue
			}
			c.mu.Lock()
			if c.closed || c.generation != generation {
				c.mu.Unlock()
				conn.Close()
				break
			}
			c.conn = conn
			for _, key := range keys {
				conn.SetWriteDeadline(time.Now().Add(c.timeout))
				err = conn.WriteJSON(struct {
					Op  string `json:"op"`
					Key Key    `json:"key"`
				}{"subscribe", key})
				if err != nil {
					break
				}
			}
			c.mu.Unlock()
			connectedAt := time.Now()
			if err == nil {
				c.read(conn, generation)
			}
			conn.Close()
			c.mu.Lock()
			if c.conn == conn {
				c.conn = nil
			}
			c.mu.Unlock()
			if time.Since(connectedAt) >= time.Second {
				backoff = 500 * time.Millisecond
			}
			c.mu.Lock()
			changed := c.generation != generation
			c.mu.Unlock()
			if changed {
				break
			}
		}
		c.mu.Lock()
		changed := c.generation != generation
		c.mu.Unlock()
		if changed {
			backoff = 500 * time.Millisecond
			continue
		}
		// Rapid-close handshakes count as failed attempts. After trying each
		// endpoint, back off rather than spinning on successful HTTP upgrades.
		delay := time.Duration(float64(backoff) * (0.8 + rand.Float64()*0.4))
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-c.wake:
			timer.Stop()
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (c *Client) read(conn *websocket.Conn, generation uint64) {
	conn.SetReadLimit(maxWireBytes)
	conn.SetReadDeadline(time.Now().Add(35 * time.Second))
	conn.SetPingHandler(func(data string) error {
		conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(c.timeout))
	})
	for {
		var value Snapshot
		if conn.ReadJSON(&value) != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(35 * time.Second))
		c.mu.Lock()
		if c.generation != generation || c.conn != conn {
			c.mu.Unlock()
			return
		}
		sub := c.subscriptions[value.Key]
		if sub == nil {
			c.mu.Unlock()
			continue
		}
		if !value.valid(value.Key) {
			c.mu.Unlock()
			return
		}
		value = c.applyLocked(value)
		c.enqueueLocked(sub, value)
		c.mu.Unlock()
	}
}
