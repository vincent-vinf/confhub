package replication_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/wire"
	"google.golang.org/protobuf/proto"
)

type standbyMirror struct {
	replication.Mirror
	attempts chan chan error
}

func (m *standbyMirror) AcquireMirrorLease(ctx context.Context, _ string, _ time.Duration) (replication.Lease, bool, error) {
	result := make(chan error)
	select {
	case m.attempts <- result:
	case <-ctx.Done():
		return replication.Lease{}, false, ctx.Err()
	}
	select {
	case err := <-result:
		return replication.Lease{}, false, err
	case <-ctx.Done():
		return replication.Lease{}, false, ctx.Err()
	}
}

func TestWorkerStandbyIntervalAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		interval, want time.Duration
	}{
		{"default", 0, 5 * time.Second},
		{"configured", 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				mirror := &standbyMirror{attempts: make(chan chan error)}
				worker := replication.NewWorker(mirror, func(context.Context) (replication.Conn, error) {
					t.Error("standby must not connect to cloud")
					return nil, errors.New("unexpected dial")
				}, 30*time.Second, tc.interval)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan struct{})
				go func() { defer close(done); worker.Run(ctx) }()
				// Startup checks immediately; subsequent checks wait the configured interval.
				result := <-mirror.attempts
				result <- nil
				synctest.Wait()
				time.Sleep(tc.want - time.Nanosecond)
				synctest.Wait()
				select {
				case <-mirror.attempts:
					t.Fatal("lease check ran before the standby interval elapsed")
				default:
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				select {
				case result = <-mirror.attempts:
				case <-time.After(time.Nanosecond):
					t.Fatal("lease check did not run at the configured interval")
				}
				result <- nil
				synctest.Wait()
				cancel()
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Fatal("cancellation did not interrupt the standby wait")
				}
			})
		})
	}
}

func TestWorkerStandbyStorageRecovery(t *testing.T) {
	mirror := &standbyMirror{attempts: make(chan chan error)}
	worker := replication.NewWorker(mirror, func(context.Context) (replication.Conn, error) {
		t.Error("standby must not connect to cloud")
		return nil, errors.New("unexpected dial")
	}, time.Second, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	defer func() { cancel(); <-done }()
	nextAttempt := func() chan error {
		t.Helper()
		select {
		case result := <-mirror.attempts:
			return result
		case <-ctx.Done():
			t.Fatal("worker did not retry lease acquisition")
			return nil
		}
	}
	result := nextAttempt()
	storageFailure := errors.New("temporary database failure")
	for _, step := range []struct {
		name string
		err  error
		want string
	}{
		{"storage failure", storageFailure, "local_storage_unavailable"},
		{"ongoing failure", storageFailure, "local_storage_unavailable"},
		{"recovered as standby", nil, ""},
		{"remains standby", nil, ""},
		{"storage fails again", storageFailure, "local_storage_unavailable"},
		{"rebuild required", replication.ErrSourceChanged, "source_changed_rebuild_required"},
		{"rebuild still required", replication.ErrSourceChanged, "source_changed_rebuild_required"},
	} {
		select {
		case result <- step.err:
		case <-ctx.Done():
			t.Fatal("lease acquisition timed out")
		}
		// The next attempt starts only after the previous status was published.
		result = nextAttempt()
		status := worker.Status()
		if status.Active || status.Connected || status.Error != step.want {
			t.Fatalf("%s: got %+v, want inactive standby with error %q", step.name, status, step.want)
		}
	}
}

type failingGuardMirror struct {
	mu                  sync.Mutex
	writes              int
	guard               bool
	loseLease           bool
	renewalFailed       chan struct{}
	releasedBeforeGuard bool
}

func (m *failingGuardMirror) MirrorStatus(context.Context) (replication.Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return replication.Status{Cursor: replication.Cursor{Source: "same-source", Sequence: 100}, Ready: true, RebuildRequired: m.guard}, nil
}
func (m *failingGuardMirror) AcquireMirrorLease(_ context.Context, owner string, _ time.Duration) (replication.Lease, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.guard {
		return replication.Lease{}, false, replication.ErrSourceChanged
	}
	if m.writes == 1 {
		if m.loseLease {
			return replication.Lease{Owner: owner, Epoch: 2}, true, nil
		}
		select {
		case <-m.renewalFailed:
		default:
			close(m.renewalFailed)
			return replication.Lease{}, false, errors.New("temporary renewal failure")
		}
	}
	return replication.Lease{Owner: owner, Epoch: 1}, true, nil
}
func (m *failingGuardMirror) RequireMirrorRebuild(ctx context.Context, _ replication.Lease) error {
	m.mu.Lock()
	m.writes++
	first := m.writes == 1
	if !first {
		m.guard = true
	}
	m.mu.Unlock()
	if first {
		if m.loseLease {
			return replication.ErrLeaseLost
		}
		select {
		case <-m.renewalFailed:
		case <-ctx.Done():
		}
		return errors.New("temporary persistence failure")
	}
	return nil
}
func (m *failingGuardMirror) ReleaseMirrorLease(context.Context, replication.Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.guard && !m.loseLease {
		m.releasedBeforeGuard = true
	}
	return nil
}
func (*failingGuardMirror) ApplyMirror(context.Context, replication.Lease, replication.Manifest, io.Reader) error {
	return errors.New("must not import a divergent source")
}

func TestWorkerRetriesRebuildGuardDespiteLeaseRenewalFailure(t *testing.T) {
	for _, loseLease := range []bool{false, true} {
		name := "renewal_failure"
		if loseLease {
			name = "new_ownership"
		}
		t.Run(name, func(t *testing.T) { testWorkerRetriesRebuildGuard(t, loseLease) })
	}
}

func testWorkerRetriesRebuildGuard(t *testing.T, loseLease bool) {
	mirror := &failingGuardMirror{renewalFailed: make(chan struct{}), loseLease: loseLease}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var dials atomic.Int32
	dial := func(context.Context) (replication.Conn, error) {
		dials.Add(1)
		a, b := replication.Pipe()
		go func() {
			defer a.Close()
			raw, _ := proto.Marshal(&wire.Frame{Version: replication.ProtocolVersion, Type: wire.FrameType_FRAME_TYPE_WATERMARK,
				Cursor: &wire.Cursor{Source: "same-source", Sequence: 50}})
			_ = a.Send(ctx, raw)
			_, _ = a.Recv(ctx) // receipt heartbeat before rollback rejection
		}()
		return b, nil
	}
	worker := replication.NewWorker(mirror, dial, 90*time.Millisecond, 250*time.Millisecond)
	done := make(chan struct{})
	go func() { defer close(done); worker.Run(ctx) }()
	defer func() { cancel(); <-done }()
	for {
		mirror.mu.Lock()
		guard, earlyRelease := mirror.guard, mirror.releasedBeforeGuard
		mirror.mu.Unlock()
		if earlyRelease || dials.Load() > 1 {
			t.Fatal("worker forgot pending rebuild after storage/renewal failure")
		}
		if guard {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("worker did not persist rebuild guard after storage recovery")
		case <-time.After(time.Millisecond):
		}
	}
}
