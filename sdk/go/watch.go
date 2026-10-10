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

type subscription struct {
	ctx     context.Context
	cancel  context.CancelFunc
	pending chan Snapshot
	last    *Snapshot
}

// Subscribe registers one callback per key, up to ten keys per client. The
// initial snapshot may be offline; a missing key delivers a deletion snapshot.
// Callbacks run independently of network receive and must honor their context.
func (c *Client) Subscribe(ctx context.Context, key Key, callback Callback) error {
	key, err := key.normalized()
	if err != nil {
		return err
	}
	if callback == nil {
		return fmt.Errorf("confhub: callback is required")
	}
	value, err := c.Get(ctx, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, exists := c.subscriptions[key]; exists {
		return fmt.Errorf("confhub: key already subscribed")
	}
	if len(c.subscriptions) >= 10 {
		return fmt.Errorf("confhub: at most ten subscriptions per client")
	}
	lifetime, cancel := context.WithCancel(ctx)
	sub := &subscription{ctx: lifetime, cancel: cancel, pending: make(chan Snapshot, 1)}
	c.subscriptions[key] = sub
	c.enqueueLocked(sub, value)
	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
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
				if sub.ctx.Err() != nil {
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
	go func() {
		defer c.wg.Done()
		select {
		case <-c.ctx.Done():
			cancel()
		case <-sub.ctx.Done():
		}
	}()
	c.resetLocked()
	return nil
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
	if sub := c.subscriptions[key]; sub != nil {
		delete(c.subscriptions, key)
		sub.cancel()
		c.resetLocked()
	}
	return nil
}

func (c *Client) enqueueLocked(sub *subscription, value Snapshot) {
	if sub.last != nil && sub.last.ID == value.ID && sub.last.Version == value.Version && sub.last.Deleted == value.Deleted {
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
		c.mu.Unlock()
		if len(keys) == 0 {
			select {
			case <-c.ctx.Done():
				return
			case <-c.wake:
				continue
			}
		}
		connected := false
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
			connected = true
			if err == nil {
				c.read(conn, generation)
			}
			conn.Close()
			c.mu.Lock()
			if c.conn == conn {
				c.conn = nil
			}
			c.mu.Unlock()
			break
		}
		c.mu.Lock()
		changed := c.generation != generation
		c.mu.Unlock()
		if changed {
			backoff = 500 * time.Millisecond
			continue
		}
		// Try the next endpoint immediately after a live connection drops. If all
		// endpoints fail, exponentially back off with jitter, capped at thirty seconds.
		if connected {
			backoff = 500 * time.Millisecond
			continue
		}
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
	conn.SetReadLimit(2 << 20)
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
