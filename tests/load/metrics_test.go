package main

import (
	"context"
	"sync"
	"testing"
	"time"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

func TestLatencyPercentilesIncludeTail(t *testing.T) {
	values := make([]time.Duration, 100)
	for i := range values {
		values[i] = time.Duration(i+1) * time.Millisecond
	}
	got := summarize(values)
	if got.Count != 100 || got.P50 != 50 || got.P95 != 95 || got.P99 != 99 || got.Max != 100 {
		t.Fatalf("tail lost: %+v", got)
	}
	if summarize(nil).Count != 0 {
		t.Fatal("empty distribution fabricated samples")
	}
}

func TestOpenLoopDoesNotReduceOfferedLoadWhenRejected(t *testing.T) {
	var c counters
	schedule(context.Background(), 30*time.Millisecond, 1000, func(int, time.Time) bool { return false }, &c)
	if c.Scheduled != 30 || c.Dropped != 30 || c.failureRate() != 1 {
		t.Fatalf("blocked receiver hid offered load: scheduled=%d dropped=%d", c.Scheduled, c.Dropped)
	}
}

func TestConcurrentRequestErrorsAndQueueDropsShareDenominator(t *testing.T) {
	c := counters{Scheduled: 100, Dropped: 10}
	var wg sync.WaitGroup
	for i := 0; i < 90; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			status := 200
			if i < 10 {
				err = context.DeadlineExceeded
				status = 0
			}
			now := time.Now()
			c.record(now, now, status, err)
		}(i)
	}
	wg.Wait()
	c.finish()
	if c.OK != 80 || c.Errors != 10 || c.Latency.Count != 90 || c.failureRate() != .2 {
		t.Fatalf("incorrect overload denominator: ok=%d errors=%d samples=%d rate=%f", c.OK, c.Errors, c.Latency.Count, c.failureRate())
	}
}

func TestNotificationCoverageOnlyCountsCommittedMeasuredPublications(t *testing.T) {
	b := book{publications: map[int64]*publication{1: {expected: 10, measured: true, success: true, seen: map[int]bool{1: true, 2: true}, latencies: []time.Duration{time.Millisecond, 400 * time.Millisecond}}, 2: {expected: 100, measured: false, success: true, seen: map[int]bool{}}, 3: {expected: 100, measured: true, success: false, seen: map[int]bool{}}}}
	got := b.result()
	if got.Expected != 10 || got.Received != 2 || got.Unobserved != 8 || got.Latency.P99 != 400 {
		t.Fatalf("missing notifications hidden by warmup or failures: %+v", got)
	}
}

func TestNotificationTimestampExcludesCollectionDelay(t *testing.T) {
	r := runner{size: 32, book: book{publications: map[int64]*publication{}, latest: map[int]sdk.Snapshot{}}}
	start := time.Now().Add(-time.Second)
	r.book.publications[1] = &publication{start: start, config: 0, expected: 1, measured: true, success: true, seen: map[int]bool{}}
	key := sdk.Key{Namespace: "test", Group: "load", Name: "one"}
	event := observation{client: 0, config: 0, key: key, id: "id", received: start.Add(50 * time.Millisecond), value: sdk.Snapshot{ID: "id", Key: key, Content: r.payload(1)}}
	r.observe(event)
	got := r.book.result()
	if got.Latency.P99 != 50 || got.Received != 1 || got.Invalid != 0 {
		t.Fatalf("collector delay contaminated timestamp: %+v", got)
	}
	r.observe(event)
	if r.book.result().Received != 1 {
		t.Fatal("duplicate callback inflated coverage")
	}
}
