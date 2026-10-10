package syncer_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/syncer"
	"gitlab.bodesitech.com/bodesi/confhub/internal/testutil"
)

func TestIndependentReplicasConvergeAndUnaffectedGrayClientStaysPinned(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service"}
	m, err := store.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = store.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "pin", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	a := syncer.New(store, syncer.Options{PollInterval: 10 * time.Millisecond, FailureTimeout: 100 * time.Millisecond, CacheBytes: 1024})
	b := syncer.New(store, syncer.Options{PollInterval: 10 * time.Millisecond, FailureTimeout: 100 * time.Millisecond, CacheBytes: 1024})
	go a.Run(ctx)
	go b.Run(ctx)
	deadline := time.Now().Add(time.Second)
	for !a.Ready() || !b.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("replicas not ready")
		}
		time.Sleep(time.Millisecond)
	}
	normal, err := b.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer normal.Close()
	gray, err := b.NewSession(map[string]string{"env": "gray"})
	if err != nil {
		t.Fatal(err)
	}
	defer gray.Close()
	if err = normal.Subscribe(ctx, k); err != nil {
		t.Fatal(err)
	}
	if err = gray.Subscribe(ctx, k); err != nil {
		t.Fatal(err)
	}
	nextCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	first, err := normal.Next(nextCtx)
	if err != nil || first.Version != 1 {
		t.Fatalf("initial state: %+v %v", first, err)
	}
	if _, err = gray.Next(nextCtx); err != nil {
		t.Fatal(err)
	}
	m, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "two", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wake()
	second, err := normal.Next(nextCtx)
	if err != nil || second.Version != 2 || second.Content != "two" {
		t.Fatalf("cross-replica update: %+v %v", second, err)
	}
	quietCtx, quietCancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer quietCancel()
	if _, err = gray.Next(quietCtx); err == nil {
		t.Fatal("unaffected gray client notified")
	}
	for _, content := range []string{"beta-one", "beta-two"} {
		m, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, RuleID: "pin", Content: content, Format: "text"})
		if err != nil {
			t.Fatal(err)
		}
		a.Wake()
		beta, err := gray.Next(nextCtx)
		if err != nil || beta.Version != 1 || beta.RuleID != "pin" || beta.Content != content {
			t.Fatalf("same-name beta update: %+v %v", beta, err)
		}
	}
	// A format change with identical text still changes the effective payload.
	m, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, RuleID: "pin", Content: "beta-two", Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wake()
	formatted, err := gray.Next(nextCtx)
	if err != nil || formatted.Version != 1 || formatted.Content != "beta-two" || formatted.Format != "yaml" {
		t.Fatalf("format-only beta update: %+v %v", formatted, err)
	}
	m, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, RuleID: "pin", Content: "beta-two", Format: "yaml", Description: "metadata only"})
	if err != nil {
		t.Fatal(err)
	}
	a.Wake()
	quietGray, cancelGray := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelGray()
	if _, err = gray.Next(quietGray); err == nil {
		t.Fatal("description-only edit notified gray client")
	}

	quietNormal, cancelNormal := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancelNormal()
	if _, err = normal.Next(quietNormal); err == nil {
		t.Fatal("beta edit notified unaffected global client")
	}

}

type outageSource struct {
	syncer.Source
	failed atomic.Bool
}

func (s *outageSource) Changes(ctx context.Context, after int64, limit int) (config.Changes, error) {
	if s.failed.Load() {
		return config.Changes{}, errors.New("database unavailable")
	}
	return s.Source.Changes(ctx, after, limit)
}
func (s *outageSource) Current(ctx context.Context, k config.Key) (*config.State, int64, error) {
	if s.failed.Load() {
		return nil, 0, errors.New("database unavailable")
	}
	return s.Source.Current(ctx, k)
}
func waitReady(t *testing.T, h *syncer.Hub, value bool) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for h.Ready() != value {
		if time.Now().After(end) {
			t.Fatalf("readiness did not become %v", value)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestDatabaseOutageClosesSessionsAndRecoveryLoadsCurrentState(t *testing.T) {
	store := testutil.Store(t)
	source := &outageSource{Source: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := syncer.New(source, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: 200 * time.Millisecond, CacheBytes: 4096})
	go h.Run(ctx)
	waitReady(t, h, true)
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service"}
	m, err := store.Save(ctx, k, config.Edit{Content: "before", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Subscribe(ctx, k); err != nil {
		t.Fatal(err)
	}
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err = session.Next(readCtx); err != nil {
		t.Fatal(err)
	}
	source.failed.Store(true)
	waitReady(t, h, false)
	select {
	case <-session.Done():
	case <-readCtx.Done():
		t.Fatal("failed replica retained client connection")
	}
	m, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "after", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	source.failed.Store(false)
	waitReady(t, h, true)
	recovered, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err = recovered.Subscribe(ctx, k); err != nil {
		t.Fatal(err)
	}
	value, err := recovered.Next(readCtx)
	if err != nil || value.Content != "after" || value.Version != 2 {
		t.Fatalf("recovery used stale cache: %+v %v", value, err)
	}
}
func TestExpiredLogTriggersSnapshotCompensation(t *testing.T) {
	store := testutil.Store(t)
	source := &outageSource{Source: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := syncer.New(source, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: time.Second, CacheBytes: 4096})
	go h.Run(ctx)
	waitReady(t, h, true)
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service"}
	m, err := store.Save(ctx, k, config.Edit{Content: "before", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.Subscribe(ctx, k); err != nil {
		t.Fatal(err)
	}
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	if _, err = session.Next(readCtx); err != nil {
		t.Fatal(err)
	}
	source.failed.Store(true)
	m, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "after", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Cleanup(ctx, "maintenance", 100, time.Nanosecond, 1000); err != nil {
		t.Fatal(err)
	}
	source.failed.Store(false)
	value, err := session.Next(readCtx)
	if err != nil || value.Content != "after" {
		t.Fatalf("missed retained-log gap: %+v %v", value, err)
	}
}

func TestPendingSnapshotCannotOverrideExpiredReadDeadline(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := syncer.New(store, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: time.Second})
	go h.Run(ctx)
	waitReady(t, h, true)
	session, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err = session.Subscribe(ctx, config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "missing"}); err != nil {
		t.Fatal(err)
	}
	expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	if _, err = session.Next(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending snapshot ignored heartbeat deadline: %v", err)
	}
}

type blackholeSource struct {
	syncer.Source
	blocked atomic.Bool
}

func (s *blackholeSource) Changes(ctx context.Context, after int64, limit int) (config.Changes, error) {
	if s.blocked.Load() {
		<-ctx.Done()
		return config.Changes{}, ctx.Err()
	}
	return s.Source.Changes(ctx, after, limit)
}
func TestSlowDatabaseFailureExpiresReadinessWithoutWaitingForAnotherPoll(t *testing.T) {
	store := testutil.Store(t)
	source := &blackholeSource{Source: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failureTimeout := 200 * time.Millisecond
	h := syncer.New(source, syncer.Options{PollInterval: 100 * time.Millisecond, FailureTimeout: failureTimeout})
	go h.Run(ctx)
	waitReady(t, h, true)
	source.blocked.Store(true)
	start := time.Now()
	waitReady(t, h, false)
	if elapsed := time.Since(start); elapsed > failureTimeout+150*time.Millisecond {
		t.Fatalf("blackholed DB extended readiness: %s", elapsed)
	}
}

type delayedReadSource struct {
	syncer.Source
	delay             atomic.Bool
	captured, release chan struct{}
}

func (s *delayedReadSource) Current(ctx context.Context, k config.Key) (*config.State, int64, error) {
	state, seq, err := s.Source.Current(ctx, k)
	if s.delay.CompareAndSwap(true, false) {
		close(s.captured)
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-s.release:
		}
	}
	return state, seq, err
}
func TestLateInitialSnapshotCannotOverwriteNewerPublication(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service"}
	m, err := store.Save(ctx, k, config.Edit{Content: "old", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	source := &delayedReadSource{Source: store, captured: make(chan struct{}), release: make(chan struct{})}
	source.delay.Store(true)
	h := syncer.New(source, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: time.Second, CacheBytes: 4096})
	go h.Run(ctx)
	waitReady(t, h, true)
	session, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	subscribed := make(chan error, 1)
	go func() { subscribed <- session.Subscribe(ctx, k) }()
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	select {
	case <-source.captured:
	case <-readCtx.Done():
		t.Fatal("initial read did not start")
	}
	_, err = store.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "new", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := session.Next(readCtx)
	if err != nil || value.Content != "new" {
		t.Fatalf("new publication: %+v %v", value, err)
	}
	close(source.release)
	if err = <-subscribed; err != nil {
		t.Fatal(err)
	}
	quiet, quietStop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer quietStop()
	if value, err = session.Next(quiet); err == nil {
		t.Fatalf("late old snapshot overwrote new publication: %+v", value)
	}
}
func TestSubscriptionLimitAndUnsubscribeReleaseCapacity(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := syncer.New(store, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: time.Second})
	go h.Run(ctx)
	waitReady(t, h, true)
	session, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for i := 0; i < 10; i++ {
		k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: fmt.Sprintf("service-%d", i)}
		if err = session.Subscribe(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	extra := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "extra"}
	if err = session.Subscribe(ctx, extra); !errors.Is(err, config.ErrInvalid) {
		t.Fatal("subscription limit not enforced")
	}
	session.Unsubscribe(config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service-0"})
	if err = session.Subscribe(ctx, extra); err != nil {
		t.Fatal("unsubscribe did not release capacity")
	}
}
