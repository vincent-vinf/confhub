package syncer_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/syncer"
)

// A slow but healthy public Source makes a complete refresh exceed one poll's
// deadline. Successful reads must keep the session alive and retain progress.
type slowResetSource struct {
	reset   atomic.Bool
	changed atomic.Pointer[config.Key]
}

func (s *slowResetSource) Changes(ctx context.Context, after int64, limit int) (config.Changes, error) {
	if s.reset.Load() {
		if k := s.changed.Load(); k != nil {
			changes := config.Changes{Sequence: 3, PurgedThrough: 2}
			if after < 3 {
				changes.Events = []config.Event{{Key: *k, Sequence: 3}}
			}
			return changes, ctx.Err()
		}
		return config.Changes{Sequence: 2, PurgedThrough: 2}, ctx.Err()
	}
	return config.Changes{Sequence: 1}, ctx.Err()
}

func (s *slowResetSource) Current(ctx context.Context, k config.Key) (*config.State, int64, error) {
	n := int64(1)
	if s.reset.Load() {
		n = 2
		timer := time.NewTimer(12 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-timer.C:
		}
		if changed := s.changed.Load(); changed != nil && *changed == k {
			n = 3
		}
	}
	return &config.State{Key: k, ID: k.Name, Sequence: n, Revision: n, GlobalVersion: n,
		Versions: map[int64]config.Version{n: {Number: n, Content: fmt.Sprint(n), Format: "text"}}}, n, nil
}

func TestSlowManyKeyResetRetainsProgressAndReadiness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	source := &slowResetSource{}
	h := syncer.New(source, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: 200 * time.Millisecond, CacheBytes: 1 << 20})
	done := make(chan struct{})
	go func() { defer close(done); h.Run(ctx) }()
	defer func() { cancel(); <-done }()
	waitReady(t, h, true)
	session, err := h.NewSession(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	const keys = 96
	for i := 0; i < keys; i++ {
		k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: fmt.Sprint(i)}
		if err = session.Subscribe(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < keys; i++ {
		if _, err = session.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	source.reset.Store(true)
	h.Wake()
	seen := map[config.Key]bool{}
	updated := false
	for len(seen) < keys {
		v, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("healthy reset disconnected or failed after %d/%d keys: %v", len(seen), keys, err)
		}
		if v.Version < 2 || v.Content != fmt.Sprint(v.Version) || !h.Ready() {
			t.Fatalf("reset did not converge while ready: %+v", v)
		}
		if source.changed.Load() == nil {
			// Update an already refreshed key while the rest are still pending.
			source.changed.Store(&v.Key)
		}
		updated = updated || v.Version == 3
		seen[v.Key] = true
	}
	if !updated {
		v, err := session.Next(ctx)
		if err != nil || v.Key != *source.changed.Load() || v.Version != 3 {
			t.Fatalf("event during compensation was skipped: %+v %v", v, err)
		}
	}
}
