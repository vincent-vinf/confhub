package replication

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Dial func(context.Context) (Conn, error)
type ConnectionStatus struct {
	Active    bool   `json:"active_sync_worker"`
	Connected bool   `json:"connected"`
	Observed  Cursor `json:"observed_cloud_cursor"`
	Error     string `json:"error,omitempty"`
}
type Worker struct {
	mirror        Mirror
	dial          Dial
	leaseDuration time.Duration
	mu            sync.Mutex
	status        ConnectionStatus
}

func NewWorker(mirror Mirror, dial Dial, leaseDuration time.Duration) *Worker {
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}
	return &Worker{mirror: mirror, dial: dial, leaseDuration: leaseDuration}
}
func (w *Worker) Status() ConnectionStatus          { w.mu.Lock(); defer w.mu.Unlock(); return w.status }
func (w *Worker) update(fn func(*ConnectionStatus)) { w.mu.Lock(); defer w.mu.Unlock(); fn(&w.status) }
func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (w *Worker) Run(ctx context.Context) {
	owner := uuid.NewString()
	// Retain an observed divergence even if lease renewal/persistence fails.
	// A subsequent ownership attempt must persist it before opening a session.
	rebuildRequired := false
	for ctx.Err() == nil {
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		lease, ok, err := w.mirror.AcquireMirrorLease(queryCtx, owner, w.leaseDuration)
		cancel()
		if err == nil && ok {
			w.update(func(s *ConnectionStatus) {
				s.Active = true
				if !rebuildRequired {
					s.Error = ""
				}
			})
			w.runOwned(ctx, lease, &rebuildRequired)
			releaseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			_ = w.mirror.ReleaseMirrorLease(releaseCtx, lease)
			stop()
			w.update(func(s *ConnectionStatus) { s.Active = false; s.Connected = false })
		} else if err != nil {
			w.update(func(s *ConnectionStatus) {
				s.Error = "local_storage_unavailable"
				if errors.Is(err, ErrSourceChanged) {
					s.Error = "source_changed_rebuild_required"
				}
			})
		}
		if !delay(ctx, 250*time.Millisecond) {
			return
		}
	}
}
func (w *Worker) runOwned(ctx context.Context, lease Lease, rebuildRequired *bool) {
	ownerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := time.NewTicker(w.leaseDuration / 3)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			// A fenced import holds this same row through commit. Waiting for our
			// own import is safe: contenders cannot acquire a newer fence either.
			// An independent timeout would cancel legitimate large imports.
			next, ok, err := w.mirror.AcquireMirrorLease(ctx, lease.Owner, w.leaseDuration)
			if err != nil || !ok || next.Epoch != lease.Epoch {
				cancel()
				return
			}
		}
	}()
	defer func() { cancel(); <-done }()
	backoff := 250 * time.Millisecond
	for ownerCtx.Err() == nil {
		if *rebuildRequired {
			// Renewal failure must not cancel persistence or permit another dial.
			persistCtx, stop := context.WithTimeout(ownerCtx, 5*time.Second)
			err := w.mirror.RequireMirrorRebuild(persistCtx, lease)
			stop()
			if err == nil || errors.Is(err, ErrLeaseLost) {
				return
			}
			if !delay(ownerCtx, 250*time.Millisecond) {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		dialCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		conn, err := w.dial(dialCtx)
		stop()
		if err == nil {
			w.update(func(s *ConnectionStatus) { s.Connected = true; s.Error = "" })
			err = Follow(ctx, conn, w.mirror, lease, func(cursor Cursor) { w.update(func(s *ConnectionStatus) { s.Observed = cursor }) })
			conn.Close()
		}
		w.update(func(s *ConnectionStatus) {
			s.Connected = false
			s.Error = "upstream_unavailable"
			if errors.Is(err, ErrSourceChanged) {
				s.Error = "source_changed_rebuild_required"
			}
		})
		if errors.Is(err, ErrSourceChanged) {
			*rebuildRequired = true
			continue
		}
		if errors.Is(err, ErrLeaseLost) {
			return
		}
		// Bound retry pressure without tying service readiness to the WAN.
		if !delay(ctx, backoff+time.Duration(rand.Int64N(int64(backoff/2)+1))) {
			return
		}
		backoff *= 2
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
}
