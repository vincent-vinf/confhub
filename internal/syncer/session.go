package syncer

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"gitlab.bodesitech.com/bodesi/confhub/internal/clientinfo"
	"sort"
	"sync"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

// Session holds one latest pending snapshot per subscribed key. All content
// strings share immutable database/cache storage; no historical send queue exists.
type Session struct {
	hub           *Hub
	tags          map[string]string
	mu            sync.Mutex
	subscriptions map[config.Key]uint64
	generation    uint64
	last, pending map[config.Key]config.Effective
	wake, done    chan struct{}
	closed        bool
	connection    *clientinfo.Client
	delivered     map[config.Key]clientinfo.Subscription
}

func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Subscribe(ctx context.Context, k config.Key) error {
	if err := k.Validate(); err != nil {
		return err
	}
	h := s.hub
	h.mu.Lock()
	s.mu.Lock()
	if s.closed || !h.Ready() {
		s.mu.Unlock()
		h.mu.Unlock()
		return ErrUnavailable
	}
	if _, ok := s.subscriptions[k]; ok {
		s.mu.Unlock()
		h.mu.Unlock()
		return nil
	}
	if len(s.subscriptions) >= 10 {
		s.mu.Unlock()
		h.mu.Unlock()
		return fmt.Errorf("%w: at most 10 subscriptions", config.ErrInvalid)
	}
	s.generation++
	generation := s.generation
	s.subscriptions[k] = generation
	if h.subscribers[k] == nil {
		h.subscribers[k] = map[*Session]uint64{}
	}
	h.subscribers[k][s] = generation
	s.mu.Unlock()
	h.mu.Unlock()
	state, seq, err := h.current(ctx, k)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		s.Unsubscribe(k)
		return err
	}
	s.offer(k, generation, state, seq)
	return nil
}
func (s *Session) Unsubscribe(k config.Key) {
	h := s.hub
	h.mu.Lock()
	s.mu.Lock()
	delete(s.subscriptions, k)
	delete(s.last, k)
	delete(s.pending, k)
	delete(s.delivered, k)
	delete(h.subscribers[k], s)
	if len(h.subscribers[k]) == 0 {
		delete(h.subscribers, k)
	}
	s.mu.Unlock()
	h.mu.Unlock()
}
func (s *Session) Close() {
	h := s.hub
	h.mu.Lock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		delete(h.sessions, s)
		for k := range s.subscriptions {
			delete(h.subscribers[k], s)
			if len(h.subscribers[k]) == 0 {
				delete(h.subscribers, k)
			}
		}
		clear(s.pending)
		clear(s.last)
		clear(s.delivered)
		clear(s.subscriptions)
	}
	s.mu.Unlock()
	h.mu.Unlock()
}
func (s *Session) offer(k config.Key, generation uint64, state *config.State, sequence int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.subscriptions[k] != generation {
		return
	}
	previous, exists := s.last[k]
	if exists && sequence < previous.Sequence {
		return
	}
	value := config.Effective{Key: k, Sequence: sequence, Deleted: true, ID: previous.ID}
	if state != nil {
		value = config.Resolve(state, s.tags)
	}
	s.last[k] = value
	if exists && previous.ID == value.ID && previous.Version == value.Version && previous.Beta == value.Beta && previous.Deleted == value.Deleted && previous.RuleID == value.RuleID && previous.Content == value.Content && previous.Format == value.Format {
		if _, ok := s.pending[k]; ok {
			s.pending[k] = value
		}
		return
	}
	s.pending[k] = value
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Session) Next(ctx context.Context) (config.Effective, error) {
	for {
		if err := ctx.Err(); err != nil {
			return config.Effective{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return config.Effective{}, ErrUnavailable
		}
		for k, v := range s.pending {
			delete(s.pending, k)
			s.mu.Unlock()
			return v, nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return config.Effective{}, ctx.Err()
		case <-s.done:
			return config.Effective{}, ErrUnavailable
		case <-s.wake:
		}
	}
}

// Connected is called only after a successful WebSocket upgrade.
func (s *Session) Connected(sourceAddress string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.connection = &clientinfo.Client{ID: uuid.NewString(), SourceAddress: sourceAddress, ConnectedAt: time.Now().UTC(), Tags: s.tags}
	s.delivered = map[config.Key]clientinfo.Subscription{}
}

// Sent records a successful transport write, not application acknowledgement.
func (s *Session) Sent(v config.Effective) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.connection == nil {
		return
	}
	if _, ok := s.subscriptions[v.Key]; !ok {
		return
	}
	s.delivered[v.Key] = clientinfo.Subscription{Key: v.Key, ID: v.ID, Version: v.Version, Revision: v.Revision, RuleID: v.RuleID, Deleted: v.Deleted, Sent: true, Beta: v.Beta}
}

// Clients copies only public connection metadata; configuration bodies never leave.
func (h *Hub) Clients() []clientinfo.Client {
	h.mu.Lock()
	sessions := make([]*Session, 0, len(h.sessions))
	for s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	clients := []clientinfo.Client{}
	for _, s := range sessions {
		s.mu.Lock()
		if !s.closed && s.connection != nil {
			c := *s.connection
			c.Tags = map[string]string{}
			for k, v := range s.tags {
				c.Tags[k] = v
			}
			c.Subscriptions = []clientinfo.Subscription{}
			for k := range s.subscriptions {
				v, ok := s.delivered[k]
				if !ok {
					v = clientinfo.Subscription{Key: k}
				}
				c.Subscriptions = append(c.Subscriptions, v)
			}
			sort.Slice(c.Subscriptions, func(i, j int) bool {
				a, b := c.Subscriptions[i].Key, c.Subscriptions[j].Key
				if a.Namespace != b.Namespace {
					return a.Namespace < b.Namespace
				}
				if a.Group != b.Group {
					return a.Group < b.Group
				}
				return a.Name < b.Name
			})
			clients = append(clients, c)
		}
		s.mu.Unlock()
	}
	return clients
}
