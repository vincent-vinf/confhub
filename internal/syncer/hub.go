// Package syncer broadcasts the persistent change stream to local subscriptions.
package syncer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
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
	lastSuccess time.Time
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
	// A longer poll period cannot support the requested outage deadline.
	if options.FailureTimeout < 2*time.Millisecond {
		options.FailureTimeout = 2 * time.Millisecond
	}
	if options.PollInterval > options.FailureTimeout/2 {
		options.PollInterval = options.FailureTimeout / 2
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
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); h.watchReadiness(watchCtx) }()
	defer func() { stopWatch(); <-watchDone; h.offline() }()
	cursor := int64(0)
	initialized := false
	var pending *refreshBatch
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
				pending = nil
			} else {
				if cursor < changes.PurgedThrough && (pending == nil || changes.PurgedThrough > pending.purgedThrough) {
					pending = h.compensation(changes)
				} else if pending == nil && len(changes.Events) > 0 {
					keys := map[config.Key]struct{}{}
					for _, event := range changes.Events {
						keys[event.Key] = struct{}{}
					}
					pending = &refreshBatch{sequence: changes.Events[len(changes.Events)-1].Sequence, purgedThrough: changes.PurgedThrough}
					for k := range keys {
						pending.keys = append(pending.keys, k)
					}
				}
				if pending != nil {
					err = h.refreshBatch(pollCtx, pending, timeout/2)
					if err == nil && len(pending.keys) == 0 {
						// Later events must be replayed even when reads already saw them.
						cursor = pending.sequence
						pending = nil
					}
				}
			}
		}
		cancel()
		if err == nil {
			h.mu.Lock()
			h.lastSuccess = time.Now()
			h.ready.Store(true)
			h.mu.Unlock()
			if pending != nil || (len(changes.Events) == 256 && cursor < changes.Sequence) {
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

// refreshBatch retains completed work across polls. A new purge restarts the
// snapshot, while ordinary later events wait until its captured cursor is done.
type refreshBatch struct {
	keys                    []config.Key
	sequence, purgedThrough int64
}

func (h *Hub) compensation(changes config.Changes) *refreshBatch {
	h.cache.clear()
	batch := &refreshBatch{sequence: changes.Sequence, purgedThrough: changes.PurgedThrough}
	h.mu.Lock()
	for k := range h.subscribers {
		batch.keys = append(batch.keys, k)
	}
	h.mu.Unlock()
	return batch
}

func (h *Hub) refreshBatch(ctx context.Context, batch *refreshBatch, budget time.Duration) error {
	until := time.Now().Add(budget)
	for n := 0; len(batch.keys) > 0 && n < 32; n++ {
		k := batch.keys[0]
		h.cache.invalidate(k)
		if err := h.refresh(ctx, k); err != nil {
			return err
		}
		batch.keys = batch.keys[1:]
		if time.Now().After(until) {
			break
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

// A watchdog expires readiness even while a database query is still waiting.
// Its clock is independent of the poll ticker and query timeout.
func (h *Hub) watchReadiness(ctx context.Context) {
	interval := h.options.FailureTimeout / 10
	if interval > 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval <= 0 {
		interval = time.Nanosecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			h.mu.Lock()
			expired := h.Ready() && now.Sub(h.lastSuccess) >= h.options.FailureTimeout
			sessions := []*Session{}
			if expired {
				h.ready.Store(false)
				for s := range h.sessions {
					sessions = append(sessions, s)
				}
			}
			h.mu.Unlock()
			if expired {
				for _, s := range sessions {
					s.Close()
				}
				h.cache.clear()
			}
		}
	}
}
