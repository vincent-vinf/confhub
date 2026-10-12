package confhub_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	confhub "github.com/vincent-vinf/confhub/sdk/go"
)

func TestChangesOnlySubscriptionUsesOneReadAndKeepsInterveningUpdate(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "config"}
	initial := confhub.Snapshot{Key: key, ID: "config", Sequence: 1, Revision: 1, Version: 1, Content: "initial", Format: "text"}
	latest := initial
	latest.Sequence, latest.Revision, latest.Version, latest.Content = 2, 2, 2, "changed before WebSocket"
	var mu sync.Mutex
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/config" {
			mu.Lock()
			reads++
			mu.Unlock()
			json.NewEncoder(w).Encode(initial)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var command any
		if conn.ReadJSON(&command) != nil {
			return
		}
		conn.WriteJSON(initial) // The initial WS state must also be suppressed.
		conn.WriteJSON(latest)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client, err := confhub.New(confhub.Options{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	received := make(chan confhub.Snapshot, 2)
	baseline, err := client.SubscribeWithOptions(context.Background(), key, confhub.SubscribeOptions{SkipInitial: true, RequireExisting: true}, func(_ context.Context, value confhub.Snapshot) { received <- value })
	if err != nil || baseline.Content != initial.Content {
		t.Fatal("unexpected baseline", baseline, err)
	}
	select {
	case value := <-received:
		if value.Content != latest.Content {
			t.Fatal("initial notification emitted or intervening update lost", value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("update not delivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Fatal("subscription performed redundant HTTP reads", reads)
	}
}

func TestFailedReplacementPreservesExistingSubscription(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "config"}
	updates := make(chan confhub.Snapshot, 2)
	connected := make(chan struct{}, 2)
	var mu sync.Mutex
	missing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/config" {
			mu.Lock()
			deleted := missing
			mu.Unlock()
			if deleted {
				w.WriteHeader(404)
				json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, Sequence: 2, Deleted: true})
			} else {
				json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, ID: "config", Sequence: 1, Revision: 1, Version: 1, Content: "initial", Format: "text"})
			}
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var command any
		if conn.ReadJSON(&command) != nil {
			return
		}
		connected <- struct{}{}
		for update := range updates {
			if conn.WriteJSON(update) != nil {
				return
			}
		}
	}))
	defer server.Close()
	defer close(updates)
	client, err := confhub.New(confhub.Options{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	received := make(chan confhub.Snapshot, 2)
	options := confhub.SubscribeOptions{SkipInitial: true, RequireExisting: true, Replace: true}
	if _, err := client.SubscribeWithOptions(context.Background(), key, options, func(_ context.Context, value confhub.Snapshot) { received <- value }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-connected:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.SubscribeWithOptions(canceled, key, options, func(context.Context, confhub.Snapshot) {}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled replacement accepted", err)
	}
	mu.Lock()
	missing = true
	mu.Unlock()
	if _, err := client.SubscribeWithOptions(context.Background(), key, options, func(context.Context, confhub.Snapshot) { t.Error("failed replacement became active") }); !errors.Is(err, confhub.ErrNotFound) {
		t.Fatal("missing replacement accepted", err)
	}
	updates <- confhub.Snapshot{Key: key, ID: "recreated", Sequence: 3, Revision: 1, Version: 1, Content: "recreated", Format: "text"}
	select {
	case value := <-received:
		if value.Content != "recreated" {
			t.Fatal(value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original subscription was lost")
	}
}

func TestStopInsideCallbackAndWaitForCompletion(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "config"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client/config" {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, ID: "config", Sequence: 1, Revision: 1, Version: 1, Content: "value", Format: "text"})
	}))
	defer server.Close()
	client, err := confhub.New(confhub.Options{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	stopped, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	if err := client.Subscribe(context.Background(), key, func(ctx context.Context, _ confhub.Snapshot) {
		client.Stop()
		<-ctx.Done()
		close(stopped)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked inside callback")
	}
	if _, err := client.Get(context.Background(), key); !errors.Is(err, confhub.ErrClosed) {
		t.Fatal("stopped client remained usable", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := client.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Wait did not wait for the running callback", err)
	}
	unblock()
	finished, finish := context.WithTimeout(context.Background(), 3*time.Second)
	defer finish()
	if err := client.Close(finished); err != nil {
		t.Fatal("shutdown did not finish after callback returned", err)
	}
	client.Stop()
	if err := client.Unsubscribe(key); !errors.Is(err, confhub.ErrClosed) {
		t.Fatal("stopped client accepted unsubscribe", err)
	}
}
