package confhub_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"testing"
	"time"

	confhub "gitlab.bodesitech.com/bodesi/confhub/sdk/go"
)

type applicationTransport struct {
	base   http.RoundTripper
	used   atomic.Bool
	closed atomic.Bool
}

func (transport *applicationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.used.Store(true)
	return transport.base.RoundTrip(request)
}

func (transport *applicationTransport) CloseIdleConnections() {
	transport.closed.Store(true)
}

func TestClientUsesCustomDefaultTransportWithoutTakingOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(confhub.Snapshot{Key: confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "config"}, Sequence: 1, ID: "config-id", Revision: 1, Version: 1, Content: "value", Format: "text"})
	}))
	defer server.Close()
	original := http.DefaultTransport
	transport := &applicationTransport{base: original}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
	client, err := confhub.New(confhub.Options{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(context.Background()) })
	value, err := client.Get(context.Background(), confhub.Key{Name: "config"})
	if err != nil || value.Content != "value" || !transport.used.Load() {
		t.Fatal("custom default transport was not used", value, err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transport.closed.Load() {
		t.Fatal("closing client closed application-owned transport")
	}
}

func TestGetFailsOverAndPreservesRawContent(t *testing.T) {
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer unavailable.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client/config" || r.URL.Query().Get("namespace") != "public" || r.URL.Query().Get("group") != "DEFAULT_GROUP" || r.URL.Query().Get("name") != "服务.yaml" {
			t.Error("incorrect request", r.URL)
		}
		var tags map[string]string
		if json.Unmarshal([]byte(r.URL.Query().Get("tags")), &tags) != nil || tags["env"] != "gray" || tags["sys.ip"] != "10.0.0.5" || tags["sys.hostname"] == "" {
			t.Error("missing routing tags")
		}
		json.NewEncoder(w).Encode(map[string]any{"sequence": 42, "id": "instance-a", "key": map[string]string{"namespace": "public", "group": "DEFAULT_GROUP", "name": "服务.yaml"}, "revision": 7, "version": 3, "content": "port: 8080\n", "format": "yaml", "deleted": false})
	}))
	defer healthy.Close()
	client, err := confhub.New(confhub.Options{Addresses: []string{unavailable.URL, healthy.URL}, Tags: map[string]string{"env": "gray", "sys.ip": "10.0.0.5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	value, err := client.Get(context.Background(), confhub.Key{Name: "服务.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if value.Content != "port: 8080\n" || value.Version != 3 || value.Source != confhub.Online || value.ID != "instance-a" {
		t.Fatalf("unexpected snapshot: %#v", value)
	}
}

func TestWatchAcceptsLowerVersionsDeletesAndRecreates(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "watched"}
	initial := confhub.Snapshot{Key: key, Sequence: 1, ID: "first", Revision: 1, Version: 5, Content: "five", Format: "text"}
	updates := make(chan confhub.Snapshot, 8)
	subscribed := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/config" {
			json.NewEncoder(w).Encode(initial)
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var message struct {
			Op  string
			Key confhub.Key
		}
		if conn.ReadJSON(&message) != nil {
			return
		}
		subscribed <- struct{}{}
		conn.WriteJSON(initial)
		for value := range updates {
			if conn.WriteJSON(value) != nil {
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
	received := make(chan confhub.Snapshot, 10)
	if err = client.Subscribe(context.Background(), key, func(ctx context.Context, value confhub.Snapshot) { received <- value }); err != nil {
		t.Fatal(err)
	}
	next := func(version int64, id string, deleted bool) confhub.Snapshot {
		t.Helper()
		select {
		case value := <-received:
			if value.Version != version || value.ID != id || value.Deleted != deleted {
				t.Fatalf("unexpected callback %#v", value)
			}
			return value
		case <-time.After(3 * time.Second):
			t.Fatal("callback timeout")
		}
		return confhub.Snapshot{}
	}
	next(5, "first", false)
	<-subscribed
	updates <- confhub.Snapshot{Key: key, Sequence: 2, ID: "first", Revision: 2, Version: 0, Beta: true, Content: "gray two", Format: "text", RuleID: "gray"}
	next(0, "first", false)
	// An older HTTP response must not roll the live state back.
	value, err := client.Get(context.Background(), key)
	if err != nil || (value.Version != 0 || !value.Beta) {
		t.Fatal("late HTTP response overwrote push", value, err)
	}
	updates <- confhub.Snapshot{Key: key, Sequence: 3, ID: "first", Revision: 3, Version: 0, Beta: true, Content: "beta replaced", Format: "text", RuleID: "gray"}
	if got := next(0, "first", false); got.Content != "beta replaced" {
		t.Fatal("same-version content was not delivered", got)
	}
	updates <- confhub.Snapshot{Key: key, Sequence: 4, ID: "first", Revision: 4, Deleted: true}
	next(0, "first", true)
	if _, err = client.Get(context.Background(), key); !errors.Is(err, confhub.ErrNotFound) {
		t.Fatal("old HTTP result resurrected deleted config", err)
	}
	updates <- confhub.Snapshot{Key: key, Sequence: 5, ID: "rebuilt", Revision: 1, Version: 1, Content: "rebuilt", Format: "text"}
	next(1, "rebuilt", false)
	if err = client.Unsubscribe(key); err != nil {
		t.Fatal(err)
	}
	if err = client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = client.Get(context.Background(), key); !errors.Is(err, confhub.ErrClosed) {
		t.Fatal("closed client allowed get", err)
	}
}

func TestSlowCallbackDoesNotBlockReceiveAndReconnectRestoresSubscription(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "slow"}
	var generation atomic.Int32
	var unavailable atomic.Bool
	var connections sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/config" {
			if unavailable.Load() {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, Sequence: 1, ID: "same", Revision: 1, Version: 1, Content: "one", Format: "text"})
			return
		}
		connections.Add(1)
		defer connections.Done()
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var command any
		if conn.ReadJSON(&command) != nil {
			return
		}
		number := generation.Add(1)
		if number == 1 {
			conn.WriteJSON(confhub.Snapshot{Key: key, Sequence: 2, ID: "same", Revision: 2, Version: 2, Content: "two", Format: "text"})
			return // Disconnect forces resubscription.
		}
		conn.WriteJSON(confhub.Snapshot{Key: key, Sequence: 3, ID: "same", Revision: 3, Version: 3, Content: "three", Format: "text"})
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
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
	started := make(chan struct{})
	release := make(chan struct{})
	received := make(chan confhub.Snapshot, 4)
	if err = client.Subscribe(context.Background(), key, func(ctx context.Context, v confhub.Snapshot) {
		if v.Version == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
		}
		received <- v
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	deadline := time.Now().Add(4 * time.Second)
	for generation.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if generation.Load() < 2 {
		t.Fatal("did not reconnect while callback blocked")
	}
	unavailable.Store(true)
	for {
		value, err := client.Get(context.Background(), key)
		if err == nil && value.Version == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receive blocked by callback", value, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	if err = client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	connections.Wait()
}

func TestCacheSurvivesRestartIsScopedAndDeletionClearsIt(t *testing.T) {
	var mode atomic.Int32
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "cached"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			w.WriteHeader(503)
		case 2:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, Sequence: 43, Deleted: true})
		default:
			json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, Sequence: 42, ID: "original", Revision: 1, Version: 0, Beta: true, RuleID: "gray", Content: "original\n", Format: "text"})
		}
	}))
	defer server.Close()
	options := confhub.Options{Addresses: []string{server.URL}, Tags: map[string]string{"env": "blue"}, CacheDir: t.TempDir()}
	client, err := confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Get(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	mode.Store(1)
	memory, err := client.Get(context.Background(), key)
	if err != nil || memory.Source != confhub.Memory || memory.Content != "original\n" {
		t.Fatal("memory fallback failed", memory, err)
	}
	client.Close(context.Background())
	client, err = confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	disk, err := client.Get(context.Background(), key)
	if err != nil || disk.Source != confhub.Disk || disk.Content != "original\n" || !disk.Beta || disk.Version != 0 {
		t.Fatal("disk fallback failed", disk, err)
	}
	different := options
	different.Tags = map[string]string{"env": "green"}
	isolated, err := confhub.New(different)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close(context.Background())
	if _, err = isolated.Get(context.Background(), key); !errors.Is(err, confhub.ErrUnavailable) {
		t.Fatal("cache leaked across tags", err)
	}
	mode.Store(2)
	deleted, err := client.Get(context.Background(), key)
	if !errors.Is(err, confhub.ErrNotFound) || !deleted.Deleted {
		t.Fatal("deletion not applied", deleted, err)
	}
	mode.Store(1)
	if _, err = client.Get(context.Background(), key); !errors.Is(err, confhub.ErrNotFound) {
		t.Fatal("deleted memory resurrected", err)
	}
	restarted, err := confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	if _, err = restarted.Get(context.Background(), key); !errors.Is(err, confhub.ErrUnavailable) {
		t.Fatal("deleted disk resurrected", err)
	}
}

func TestUnavailableWithoutCacheAndContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	client, err := confhub.New(confhub.Options{Addresses: []string{server.URL}, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	if _, err = client.Get(context.Background(), confhub.Key{Name: "absent"}); !errors.Is(err, confhub.ErrUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.Get(ctx, confhub.Key{Name: "absent"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestWatchBacksOffWhenServerImmediatelyClosesConnections(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "retry"}
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/config" {
			json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, ID: "a", Sequence: 1, Revision: 1, Version: 1, Content: "one", Format: "text"})
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		attempts.Add(1)
		conn.Close()
	}))
	defer server.Close()
	client, err := confhub.New(confhub.Options{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	if err = client.Subscribe(context.Background(), key, func(context.Context, confhub.Snapshot) {}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if count := attempts.Load(); count > 2 {
		t.Fatalf("rapid-close server caused reconnect storm: %d attempts in 150ms", count)
	}
}

func TestMaximumEscapedContentSurvivesWatchAndDiskRestart(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "large"}
	value := confhub.Snapshot{Key: key, ID: "large", Sequence: 1, Revision: 1, Version: 1, Content: strings.Repeat("\x00", 1<<20), Format: "text"}
	var offline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/client/config" {
			if offline.Load() {
				w.WriteHeader(503)
				return
			}
			json.NewEncoder(w).Encode(value)
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
		next := value
		next.Sequence = 2
		next.Version = 2
		next.Revision = 2
		conn.WriteJSON(next)
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	options := confhub.Options{Addresses: []string{server.URL}, CacheDir: t.TempDir()}
	client, err := confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	received := make(chan confhub.Snapshot, 2)
	if err = client.Subscribe(context.Background(), key, func(ctx context.Context, v confhub.Snapshot) { received <- v }); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case v := <-received:
			if len(v.Content) != 1<<20 {
				t.Fatal("content truncated")
			}
			if v.Version == 2 {
				goto done
			}
		case <-deadline:
			t.Fatal("large watch snapshot not received")
		}
	}
done:
	client.Close(context.Background())
	offline.Store(true)
	restarted, err := confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	cached, err := restarted.Get(context.Background(), key)
	if err != nil || len(cached.Content) != 1<<20 || cached.Source != confhub.Disk {
		t.Fatal("large disk cache rejected", err)
	}
}

func TestDiskSnapshotFencesAnAlreadyRunningOlderQuery(t *testing.T) {
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "fence"}
	current := confhub.Snapshot{Key: key, ID: "same", Sequence: 42, Revision: 2, Version: 2, Content: "new", Format: "text"}
	var holdNext atomic.Bool
	var offline atomic.Bool
	held := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if holdNext.Swap(false) {
			close(held)
			<-release
			old := current
			old.Sequence = 40
			old.Revision = 1
			old.Version = 1
			old.Content = "old"
			json.NewEncoder(w).Encode(old)
			return
		}
		if offline.Load() {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(current)
	}))
	defer server.Close()
	options := confhub.Options{Addresses: []string{server.URL}, CacheDir: t.TempDir()}
	client, err := confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Get(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	client.Close(context.Background())
	client, err = confhub.New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	holdNext.Store(true)
	offline.Store(true)
	done := make(chan confhub.Snapshot, 1)
	go func() { value, _ := client.Get(context.Background(), key); done <- value }()
	<-held
	cached, err := client.Get(context.Background(), key)
	if err != nil || cached.Sequence != 42 {
		close(release)
		t.Fatal("disk cache unavailable", err)
	}
	close(release)
	select {
	case late := <-done:
		if late.Sequence != 42 || late.Content != "new" {
			t.Fatal("late response replaced newer disk snapshot", late)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late request hung")
	}
}
