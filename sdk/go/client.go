package confhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Options struct {
	Addresses []string
	Tags      map[string]string
	Timeout   time.Duration
	// CacheDir enables private, atomic disk snapshots. Empty disables persistence.
	CacheDir string
}

type Client struct {
	addresses     []string
	tags          string
	timeout       time.Duration
	http          *http.Client
	mu            sync.Mutex
	closed        bool
	ctx           context.Context
	cancel        context.CancelFunc
	records       map[Key]Snapshot
	cachePath     string
	errors        chan error
	subscriptions map[Key]*subscription
	conn          *websocket.Conn
	generation    uint64
	wake          chan struct{}
	wg            sync.WaitGroup
}

func New(options Options) (*Client, error) {
	if len(options.Addresses) == 0 {
		return nil, fmt.Errorf("confhub: at least one server address is required")
	}
	if options.Timeout < 0 {
		return nil, fmt.Errorf("confhub: timeout must be positive")
	}
	if options.Timeout == 0 {
		options.Timeout = 2 * time.Second
	}
	addresses := make([]string, 0, len(options.Addresses))
	for _, address := range options.Addresses {
		parsed, err := url.Parse(address)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("confhub: invalid server address")
		}
		addresses = append(addresses, strings.TrimRight(parsed.String(), "/"))
	}
	tags := map[string]string{}
	hostname, _ := os.Hostname()
	tags["sys.hostname"] = hostname
	tags["sys.ip"] = localIP()
	for key, value := range options.Tags {
		tags[key] = value
	}
	if len(tags) > 64 {
		return nil, fmt.Errorf("confhub: too many tags")
	}
	for key, value := range tags {
		if key == "" || len(key) > 128 || len(value) > 512 {
			return nil, fmt.Errorf("confhub: invalid tag")
		}
	}
	raw, _ := json.Marshal(tags)
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{addresses: addresses, tags: string(raw), timeout: options.Timeout, http: &http.Client{}, ctx: ctx, cancel: cancel, records: map[Key]Snapshot{}, errors: make(chan error, 1), subscriptions: map[Key]*subscription{}, wake: make(chan struct{}, 1)}
	if err := client.initCache(options.CacheDir); err != nil {
		cancel()
		return nil, err
	}
	client.wg.Add(1)
	go func() { defer client.wg.Done(); client.watch() }()
	return client, nil
}

func localIP() string {
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		if value, ok := address.(*net.IPNet); ok && !value.IP.IsLoopback() && value.IP.To4() != nil {
			return value.IP.String()
		}
	}
	return "127.0.0.1"
}

func (c *Client) endpoint(address, path string, key *Key) string {
	query := url.Values{"tags": {c.tags}}
	if key != nil {
		query.Set("namespace", key.Namespace)
		query.Set("group", key.Group)
		query.Set("name", key.Name)
	}
	return address + path + "?" + query.Encode()
}

func (c *Client) Get(ctx context.Context, key Key) (Snapshot, error) {
	key, err := key.normalized()
	if err != nil {
		return Snapshot{}, err
	}
	if err = ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return Snapshot{}, ErrClosed
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	for _, address := range c.addresses {
		requestCtx, finish := context.WithTimeout(operation, c.timeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, c.endpoint(address, "/api/client/config", &key), nil)
		if err != nil {
			finish()
			continue
		}
		response, err := c.http.Do(request)
		if err != nil {
			finish()
			continue
		}
		var snapshot Snapshot
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&snapshot)
		response.Body.Close()
		finish()
		if decodeErr != nil || !snapshot.valid(key) || (response.StatusCode != 200 && response.StatusCode != 404) || (response.StatusCode == 404) != snapshot.Deleted {
			continue
		}
		snapshot = c.accept(snapshot)
		if snapshot.Deleted {
			return snapshot, ErrNotFound
		}
		return snapshot, nil
	}
	if err = ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if c.ctx.Err() != nil {
		return Snapshot{}, ErrClosed
	}
	return c.cached(key)
}

// Errors reports the latest asynchronous persistence or callback error. It is
// bounded and never blocks network receive. The channel isn't closed by Close.
func (c *Client) Errors() <-chan error { return c.errors }

func (c *Client) report(err error) {
	select {
	case c.errors <- err:
	default:
	}
}

func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	for key, sub := range c.subscriptions {
		sub.cancel()
		delete(c.subscriptions, key)
	}
	c.resetLocked()
	c.mu.Unlock()
	c.http.CloseIdleConnections()
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
