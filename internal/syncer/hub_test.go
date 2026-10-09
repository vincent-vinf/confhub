package syncer_test

import (
	"context"
	"errors"
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
	m, err = store.SetRules(ctx, k, m.State.ID, m.State.Revision, []config.Rule{{ID: "pin", Enabled: true, TargetVersion: 1, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}})
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
	h := syncer.New(source, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: 30 * time.Millisecond, CacheBytes: 4096})
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
