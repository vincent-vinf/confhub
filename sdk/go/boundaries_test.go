package confhub_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	confhub "github.com/vincent-vinf/confhub/sdk/go"
)

func TestPublicOptionsRejectInvalidInputs(t *testing.T) {
	for _, options := range []confhub.Options{{}, {Addresses: []string{"ftp://example.com"}}, {Addresses: []string{"http://user:pass@example.com"}}, {Addresses: []string{"http://example.com?q=1"}}, {Addresses: []string{"http://example.com#fragment"}}, {Addresses: []string{"http://example.com"}, Timeout: -time.Second}, {Addresses: []string{"http://example.com"}, Tags: map[string]string{"": "x"}}, {Addresses: []string{"http://example.com"}, Tags: map[string]string{"x": strings.Repeat("x", 513)}}} {
		c, err := confhub.New(options)
		if c != nil {
			c.Close(context.Background())
		}
		if err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}
func TestSubscriptionLimitsCancellationAndConcurrentClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/client/config" {
			w.WriteHeader(503)
			return
		}
		key := confhub.Key{Namespace: r.URL.Query().Get("namespace"), Group: r.URL.Query().Get("group"), Name: r.URL.Query().Get("name")}
		json.NewEncoder(w).Encode(confhub.Snapshot{Key: key, ID: "config", Sequence: 1, Revision: 1, Version: 1, Content: "value", Format: "text"})
	}))
	defer server.Close()
	c, err := confhub.New(confhub.Options{Addresses: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	callback := func(context.Context, confhub.Snapshot) {}
	if err := c.Subscribe(context.Background(), confhub.Key{Name: "nil"}, nil); err == nil {
		t.Fatal("nil callback accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Subscribe(canceled, confhub.Key{Name: "canceled"}, callback); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled subscription accepted", err)
	}
	for i := 0; i < 10; i++ {
		if err := c.Subscribe(context.Background(), confhub.Key{Name: fmt.Sprintf("key-%d", i)}, callback); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Subscribe(context.Background(), confhub.Key{Name: "key-0"}, callback); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := c.Subscribe(context.Background(), confhub.Key{Name: "eleventh"}, callback); err == nil {
		t.Fatal("capacity exceeded")
	}
	if err := c.Unsubscribe(confhub.Key{Name: "key-0"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Subscribe(context.Background(), confhub.Key{Name: "replacement"}, callback); err != nil {
		t.Fatal("unsubscribe did not release capacity", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				errs <- c.Close(ctx)
			} else if i%3 == 1 {
				_, err := c.Get(ctx, confhub.Key{Name: "replacement"})
				if err != nil && !errors.Is(err, confhub.ErrClosed) && !errors.Is(err, context.Canceled) {
					errs <- err
				}
			} else {
				errs <- c.Unsubscribe(confhub.Key{Name: "replacement"})
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Get(ctx, confhub.Key{Name: "x"}); !errors.Is(err, confhub.ErrClosed) {
		t.Fatal("closed client remained usable", err)
	}
}
