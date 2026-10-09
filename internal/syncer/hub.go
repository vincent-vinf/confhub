// Package syncer broadcasts the persistent change stream to local subscriptions.
package syncer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

var ErrUnavailable = errors.New("configuration synchronization unavailable")

type Source interface {
	Current(context.Context, config.Key) (*config.State, int64, error)
	Changes(context.Context, int64, int) (config.Changes, error)
}
type Options struct {
	PollInterval, FailureTimeout time.Duration
	CacheBytes                   int64
}
type Hub struct {
	source      Source
	options     Options
	ready       atomic.Bool
	wake        chan struct{}
	mu          sync.Mutex
	sessions    map[*Session]struct{}
	subscribers map[config.Key]map[*Session]uint64
	cache       *stateCache
}

func New(source Source, options Options) *Hub {
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	if options.FailureTimeout <= 0 {
		options.FailureTimeout = 2 * time.Second
	}
	return &Hub{source: source, options: options, wake: make(chan struct{}, 1), sessions: map[*Session]struct{}{}, subscribers: map[config.Key]map[*Session]uint64{}, cache: newStateCache(options.CacheBytes)}
}
func (h *Hub) Ready() bool { return h.ready.Load() }
func (h *Hub) Wake() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}
func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(h.options.PollInterval)
	defer ticker.Stop()
	defer h.offline()
	cursor := int64(0)
	initialized := false
	var failedSince time.Time
	for {
		timeout := h.options.FailureTimeout / 2
		if timeout > time.Second {
			timeout = time.Second
		}
		if timeout <= 0 {
			timeout = time.Millisecond
		}
		pollCtx, cancel := context.WithTimeout(ctx, timeout)
		changes, err := h.source.Changes(pollCtx, cursor, 256)
		if err == nil {
			if !initialized || !h.Ready() {
				h.cache.clear()
				cursor = changes.Sequence
				initialized = true
				h.ready.Store(true)
			} else if cursor < changes.PurgedThrough {
				err = h.compensate(pollCtx)
				if err == nil {
					cursor = changes.Sequence
				}
			} else {
				keys := map[config.Key]struct{}{}
				for _, event := range changes.Events {
					keys[event.Key] = struct{}{}
				}
				for k := range keys {
					h.cache.invalidate(k)
					if err = h.refresh(pollCtx, k); err != nil {
						break
					}
				}
				if err == nil && len(changes.Events) > 0 {
					cursor = changes.Events[len(changes.Events)-1].Sequence
				}
			}
		}
		cancel()
		if err != nil {
			if failedSince.IsZero() {
				failedSince = time.Now()
			}
			if time.Since(failedSince) >= h.options.FailureTimeout {
				h.offline()
			}
		} else {
			failedSince = time.Time{}
			if len(changes.Events) == 256 && cursor < changes.Sequence {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-h.wake:
		}
	}
}
func (h *Hub) offline() {
	h.ready.Store(false)
	h.mu.Lock()
	sessions := make([]*Session, 0, len(h.sessions))
	for s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
	h.cache.clear()
}
func (h *Hub) compensate(ctx context.Context) error {
	h.cache.clear()
	h.mu.Lock()
	keys := make([]config.Key, 0, len(h.subscribers))
	for k := range h.subscribers {
		keys = append(keys, k)
	}
	h.mu.Unlock()
	for _, k := range keys {
		if err := h.refresh(ctx, k); err != nil {
			return err
		}
	}
	return nil
}
func (h *Hub) refresh(ctx context.Context, k config.Key) error {
	h.mu.Lock()
	targets := map[*Session]uint64{}
	for s, g := range h.subscribers[k] {
		targets[s] = g
	}
	h.mu.Unlock()
	if len(targets) == 0 {
		return nil
	}
	state, seq, err := h.current(ctx, k)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return err
	}
	for s, g := range targets {
		s.offer(k, g, state, seq)
	}
	return nil
}
func (h *Hub) Current(ctx context.Context, k config.Key) (*config.State, int64, error) {
	if !h.Ready() {
		return nil, 0, ErrUnavailable
	}
	return h.current(ctx, k)
}
func (h *Hub) current(ctx context.Context, k config.Key) (*config.State, int64, error) {
	return h.cache.load(ctx, k, h.source)
}
func (h *Hub) NewSession(tags map[string]string) (*Session, error) {
	if err := config.ValidateTags(tags); err != nil {
		return nil, err
	}
	copyTags := map[string]string{}
	for k, v := range tags {
		copyTags[k] = v
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.Ready() {
		return nil, ErrUnavailable
	}
	s := &Session{hub: h, tags: copyTags, subscriptions: map[config.Key]uint64{}, last: map[config.Key]config.Effective{}, pending: map[config.Key]config.Effective{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
	h.sessions[s] = struct{}{}
	return s, nil
}
