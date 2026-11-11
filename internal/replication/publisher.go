package replication

import (
	"context"
	"io"
	"sync"
	"time"
)

// Publisher polls once per cloud instance and broadcasts changes to all sessions.
// Run owns its lifetime; Serve never performs independent source polling.
type Publisher struct {
	source   Source
	poll     time.Duration
	once     sync.Once
	mu       sync.Mutex
	cursor   Cursor
	err      error
	ready    bool
	revision uint64
	changed  chan struct{}
}

func NewPublisher(source Source, poll time.Duration) *Publisher {
	if poll <= 0 {
		poll = time.Second
	}
	return &Publisher{source: source, poll: poll, changed: make(chan struct{})}
}

func (p *Publisher) Run(ctx context.Context) {
	p.once.Do(func() {
		ticker := time.NewTicker(p.poll)
		defer ticker.Stop()
		defer p.publish(Cursor{}, ErrClosed, 0, false)
		for {
			p.mu.Lock()
			revision := p.revision
			p.mu.Unlock()
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			cursor, err := p.source.Watermark(queryCtx)
			cancel()
			p.publish(cursor, err, revision, true)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (p *Publisher) publish(cursor Cursor, err error, revision uint64, conditional bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// An export can finish after this poll starts, with a newer consistent cursor.
	if conditional && revision != p.revision {
		return
	}
	if p.ready && p.cursor == cursor && p.err == nil && err == nil {
		return
	}
	p.cursor, p.err, p.ready = cursor, err, true
	p.revision++
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *Publisher) watermark(ctx context.Context) (Cursor, <-chan struct{}, error) {
	for {
		p.mu.Lock()
		cursor, changed, err, ready := p.cursor, p.changed, p.err, p.ready
		p.mu.Unlock()
		if ready {
			return cursor, changed, err
		}
		select {
		case <-ctx.Done():
			return Cursor{}, changed, ctx.Err()
		case <-changed:
		}
	}
}
func (p *Publisher) Watermark(ctx context.Context) (Cursor, error) {
	cursor, _, err := p.watermark(ctx)
	return cursor, err
}
func (p *Publisher) Export(ctx context.Context, after Cursor, out io.Writer) (Manifest, error) {
	batch, err := p.source.Export(ctx, after, out)
	if err == nil {
		p.mu.Lock()
		if !p.ready || (p.err == nil && p.cursor.Source == batch.Source && batch.Sequence > p.cursor.Sequence) {
			p.cursor, p.err, p.ready = batch.Cursor, nil, true
			p.revision++
			close(p.changed)
			p.changed = make(chan struct{})
		}
		p.mu.Unlock()
	}
	return batch, err
}
