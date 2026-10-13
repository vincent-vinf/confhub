// Command load measures a running ConfHub cluster through public HTTP and Go SDK APIs.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

type object = map[string]any
type state struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}
type fixture struct {
	key        sdk.Key
	state      state
	main, beta string
}
type distribution struct {
	Count int     `json:"count"`
	P50   float64 `json:"p50_ms"`
	P95   float64 `json:"p95_ms"`
	P99   float64 `json:"p99_ms"`
	Max   float64 `json:"max_ms"`
}

func summarize(values []time.Duration) distribution {
	if len(values) == 0 {
		return distribution{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	p := func(q float64) float64 { return ms(values[int(math.Ceil(q*float64(len(values))))-1]) }
	return distribution{len(values), p(.5), p(.95), p(.99), ms(values[len(values)-1])}
}

type counters struct {
	mu                 sync.Mutex
	Scheduled          int64         `json:"scheduled"`
	Dropped            int64         `json:"dropped"`
	OK                 int64         `json:"ok"`
	Errors             int64         `json:"errors"`
	Status             map[int]int64 `json:"status"`
	Latency            distribution  `json:"latency"`
	FromSchedule       distribution  `json:"from_schedule"`
	latency, scheduled []time.Duration
}

func (c *counters) record(start, planned time.Time, status int, err error) {
	c.recordAt(start, planned, time.Now(), status, err)
}
func (c *counters) recordAt(start, planned, completed time.Time, status int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Status == nil {
		c.Status = map[int]int64{}
	}
	c.Status[status]++
	if err == nil {
		c.OK++
	} else {
		c.Errors++
	}
	c.latency = append(c.latency, completed.Sub(start))
	c.scheduled = append(c.scheduled, completed.Sub(planned))
}
func (c *counters) finish() {
	c.Latency = summarize(c.latency)
	c.FromSchedule = summarize(c.scheduled)
	c.latency = nil
	c.scheduled = nil
}
func (c *counters) failureRate() float64 {
	if c.Scheduled == 0 {
		return 0
	}
	return float64(c.Errors+c.Dropped) / float64(c.Scheduled)
}

type publication struct {
	start             time.Time
	config            int
	beta              bool
	expected          int
	measured, success bool
	seen              map[int]bool
	latencies         []time.Duration
}
type subscriber struct {
	client *sdk.Client
	config int
	gray   bool
}
type book struct {
	mu           sync.Mutex
	next         int64
	publications map[int64]*publication
	latest       map[int]sdk.Snapshot
	invalid      int
}

func (b *book) register(config int, beta bool, expected int, measured bool) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	b.publications[b.next] = &publication{start: time.Now(), config: config, beta: beta, expected: expected, measured: measured, seen: map[int]bool{}}
	return b.next
}

type notificationResult struct {
	Expected        int          `json:"expected"`
	Received        int          `json:"received"`
	Unobserved      int          `json:"unobserved"`
	Invalid         int          `json:"invalid"`
	RecorderDropped int64        `json:"recorder_dropped"`
	Latency         distribution `json:"publish_start_to_callback"`
}

func (b *book) result() notificationResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := notificationResult{Invalid: b.invalid}
	var delays []time.Duration
	for _, p := range b.publications {
		if p.measured && p.success {
			out.Expected += p.expected
			out.Received += len(p.seen)
			delays = append(delays, p.latencies...)
		}
	}
	out.Unobserved = out.Expected - out.Received
	out.Latency = summarize(delays)
	return out
}

type report struct {
	Scenario        string             `json:"scenario"`
	Namespace       string             `json:"namespace"`
	Clients         int                `json:"clients"`
	Configs         int                `json:"configs"`
	PayloadBytes    int                `json:"payload_bytes"`
	GetRate         int                `json:"get_rate"`
	PublishRate     int                `json:"publish_rate"`
	DurationSeconds float64            `json:"duration_seconds"`
	ElapsedSeconds  float64            `json:"elapsed_seconds"`
	Get             counters           `json:"get"`
	Publish         counters           `json:"publish"`
	Notifications   notificationResult `json:"notifications"`
	Converged       bool               `json:"converged"`
	Pass            bool               `json:"pass"`
	Reasons         []string           `json:"reasons"`
	CleanupErrors   []string           `json:"cleanup_errors,omitempty"`
}
type observation struct {
	client, config int
	key            sdk.Key
	id             string
	gray           bool
	value          sdk.Snapshot
	received       time.Time
}

type runner struct {
	events                              chan observation
	collectorStop, collectorDone        chan struct{}
	recorderDropped                     atomic.Int64
	ctx                                 context.Context
	addresses                           []string
	admins                              []*http.Client
	http                                *http.Client
	transport                           *http.Transport
	namespace                           string
	fixtures                            []fixture
	read                                fixture
	subscribers                         []subscriber
	book                                book
	out                                 report
	duration, warmup, timeout           time.Duration
	workers, connectWorkers, size       int
	notificationP99, getP99, publishP99 time.Duration
	maxError                            float64
	output                              string
	created                             bool
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func (r *runner) api(index int, method, path string, body any, out any) (int, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, r.addresses[index%len(r.addresses)]+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", r.addresses[index%len(r.addresses)])
	res, err := r.admins[index%len(r.admins)].Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s transport failed", method, path)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return res.StatusCode, fmt.Errorf("%s %s HTTP %d", method, path, res.StatusCode)
	}
	if out != nil {
		err = json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out)
	} else {
		_, err = io.Copy(io.Discard, res.Body)
	}
	return res.StatusCode, err
}
func path(k sdk.Key) string {
	return "/api/admin/namespaces/" + url.PathEscape(k.Namespace) + "/groups/" + url.PathEscape(k.Group) + "/configs/" + url.PathEscape(k.Name)
}
func edit(f fixture) object {
	return object{"expected_id": f.state.ID, "expected_revision": f.state.Revision, "confirmed": true}
}
func (r *runner) payload(id int64) string {
	prefix := "load|" + strconv.FormatInt(id, 10) + "|"
	return prefix + strings.Repeat("x", r.size-len(prefix))
}
func (r *runner) save(index int, f *fixture, content string, beta bool) (int, error) {
	body := edit(*f)
	body["content"] = content
	body["format"] = "text"
	if beta {
		body["target"] = "beta"
	}
	var res struct {
		State   state `json:"state"`
		Changed bool  `json:"changed"`
	}
	status, err := r.api(index, "PUT", path(f.key), body, &res)
	if err == nil {
		if !res.Changed {
			return status, fmt.Errorf("load publication was unexpectedly a no-op")
		}
		f.state = res.State
		if beta {
			f.beta = content
		} else {
			f.main = content
		}
	}
	return status, err
}
func (r *runner) setup() {
	for i := range r.addresses {
		_, err := r.api(i, "GET", "/health/ready", nil, nil)
		must(err)
		_, err = r.api(i, "POST", "/api/admin/login", object{"username": "admin", "password": os.Getenv("CONFHUB_LOAD_PASSWORD")}, nil)
		must(err)
	}
	// Ownership is recorded before a request that might commit but lose its reply.
	r.created = true
	_, err := r.api(0, "POST", "/api/admin/namespaces", object{"name": r.namespace}, nil)
	must(err)
	_, err = r.api(0, "POST", "/api/admin/namespaces/"+r.namespace+"/groups", object{"name": "load"}, nil)
	must(err)
	for i := 0; i < r.out.Configs; i++ {
		f := fixture{key: sdk.Key{Namespace: r.namespace, Group: "load", Name: fmt.Sprintf("config-%d", i)}}
		r.fixtures = append(r.fixtures, f)
		mustSave := func() { _, err = r.save(0, &r.fixtures[i], r.payload(0), false); must(err) }
		mustSave()
		if r.out.Scenario == "mixed" {
			body := edit(r.fixtures[i])
			body["source_version"] = 1
			body["rules"] = []object{{"id": "gray", "name": "gray", "enabled": true, "conditions": []object{{"tag": "env", "operator": "eq", "values": []string{"gray"}}}}}
			var res struct {
				State state `json:"state"`
			}
			_, err = r.api(0, "PUT", path(f.key)+"/rules", body, &res)
			must(err)
			r.fixtures[i].state = res.State
			r.fixtures[i].beta = r.fixtures[i].main
		}
	}
	r.read = fixture{key: sdk.Key{Namespace: r.namespace, Group: "load", Name: "read-only"}}
	_, err = r.save(0, &r.read, r.payload(0), false)
	must(err)
	if r.out.Clients == 0 {
		return
	}
	r.startCollector()
	r.subscribers = make([]subscriber, r.out.Clients)
	jobs := make(chan int)
	var wg sync.WaitGroup
	errors := make(chan error, r.out.Clients)
	for w := 0; w < r.connectWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				fi := i % len(r.fixtures)
				gray := r.out.Scenario == "mixed" && (i/len(r.fixtures))%10 == 0
				env := "normal"
				if gray {
					env = "gray"
				}
				c, err := sdk.New(sdk.Options{Addresses: []string{r.addresses[i%len(r.addresses)]}, Tags: map[string]string{"run": r.namespace, "env": env}, Timeout: r.timeout})
				if err != nil {
					errors <- err
					continue
				}
				r.subscribers[i] = subscriber{c, fi, gray}
				expected := r.fixtures[fi]
				err = c.Subscribe(r.ctx, expected.key, func(_ context.Context, value sdk.Snapshot) {
					received := time.Now()
					event := observation{i, fi, expected.key, expected.state.ID, gray, value, received}
					select {
					case r.events <- event:
					default:
						r.recorderDropped.Add(1)
					}
				})
				if err != nil {
					errors <- err
				}
			}
		}()
	}
	for i := range r.subscribers {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(errors)
	for err := range errors {
		must(err)
	}
	// An initial SDK callback may come from GET. Publish distinct sentinels;
	// receiving them proves that every subscription can receive real updates.
	for i := range r.fixtures {
		_, err, _ := r.publish(i, false, false)
		must(err)
		if r.out.Scenario == "mixed" {
			_, err, _ = r.publish(i, true, false)
			must(err)
		}
	}
	if !r.converge(90 * time.Second) {
		panic("initial subscriptions did not converge within 90s")
	}
}

// Callback timestamps are captured before any collection work. A bounded,
// nonblocking queue keeps bookkeeping off the SDK callback/transport path.
func (r *runner) startCollector() {
	r.events = make(chan observation, min(100000, max(4096, r.out.Clients*4)))
	r.collectorStop, r.collectorDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(r.collectorDone)
		for {
			select {
			case event := <-r.events:
				r.observe(event)
			case <-r.collectorStop:
				for {
					select {
					case event := <-r.events:
						r.observe(event)
					default:
						return
					}
				}
			}
		}
	}()
}
func (r *runner) observe(event observation) {
	r.book.mu.Lock()
	defer r.book.mu.Unlock()
	value := event.value
	previous, exists := r.book.latest[event.client]
	if value.Key != event.key || value.ID != event.id || value.Deleted || value.Beta != event.gray || len(value.Content) != r.size || (exists && value.Sequence < previous.Sequence) {
		r.book.invalid++
	}
	r.book.latest[event.client] = value
	parts := strings.SplitN(value.Content, "|", 3)
	if len(parts) != 3 || parts[0] != "load" {
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return
	}
	p := r.book.publications[id]
	if p != nil && (p.config != event.config || p.beta != event.gray) {
		r.book.invalid++
		return
	}
	if p != nil && !p.seen[event.client] {
		p.seen[event.client] = true
		p.latencies = append(p.latencies, event.received.Sub(p.start))
	}
}

func (r *runner) publish(fi int, beta, measured bool) (int, error, time.Time) {
	expected := 0
	for _, s := range r.subscribers {
		if s.config == fi && s.gray == beta {
			expected++
		}
	}
	id := r.book.register(fi, beta, expected, measured)
	status, err := r.save(fi, &r.fixtures[fi], r.payload(id), beta)
	completed := time.Now()
	r.book.mu.Lock()
	r.book.publications[id].success = err == nil
	r.book.mu.Unlock()
	if measured && err != nil {
		return status, fmt.Errorf("publication HTTP %d", status), completed
	}
	return status, err, completed
}
func (r *runner) converge(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		all := true
		r.book.mu.Lock()
		for i, s := range r.subscribers {
			expected := r.fixtures[s.config].main
			if s.gray {
				expected = r.fixtures[s.config].beta
			}
			got, found := r.book.latest[i]
			if !found || got.Content != expected || got.Beta != s.gray || got.Deleted {
				all = false
				break
			}
		}
		r.book.mu.Unlock()
		if all {
			return true
		}
		if time.Now().After(deadline) || r.ctx.Err() != nil {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Open-loop arrivals are fixed by elapsed time, independent of response speed.
// Full bounded worker queues are reported as dropped offered operations.
func schedule(ctx context.Context, duration time.Duration, rate int, offer func(int, time.Time) bool, c *counters) {
	if rate == 0 {
		return
	}
	start := time.Now()
	total := int(duration.Seconds() * float64(rate))
	sent := 0
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for sent < total {
		due := min(total, int(time.Since(start).Seconds()*float64(rate)))
		for sent < due {
			planned := start.Add(time.Duration(float64(sent+1) / float64(rate) * float64(time.Second)))
			ok := offer(sent, planned)
			c.mu.Lock()
			c.Scheduled++
			if !ok {
				c.Dropped++
			}
			c.mu.Unlock()
			sent++
		}
		if sent == total {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (r *runner) gets(duration time.Duration, rate int, c *counters) {
	if rate == 0 {
		return
	}
	type job struct {
		index   int
		planned time.Time
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < r.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				start := time.Now()
				ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
				k := r.read.key
				u := r.addresses[job.index%len(r.addresses)] + "/api/client/config?" + url.Values{"namespace": {k.Namespace}, "group": {k.Group}, "name": {k.Name}}.Encode()
				req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
				status := 0
				if err == nil {
					var res *http.Response
					res, err = r.http.Do(req)
					if err == nil {
						status = res.StatusCode
						var value sdk.Snapshot
						err = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&value)
						res.Body.Close()
						if err == nil && (status != 200 || value.Key != k || value.ID != r.read.state.ID || value.Content != r.read.main || value.Deleted) {
							err = fmt.Errorf("GET status or contents mismatch")
						}
					}
				}
				cancel()
				c.record(start, job.planned, status, err)
			}
		}()
	}
	schedule(r.ctx, duration, rate, func(i int, p time.Time) bool {
		select {
		case jobs <- job{i, p}:
			return true
		default:
			return false
		}
	}, c)
	close(jobs)
	wg.Wait()
}
func (r *runner) publishes(duration time.Duration, rate int, c *counters, measured bool) {
	if rate == 0 {
		return
	}
	type job struct {
		beta    bool
		planned time.Time
	}
	queues := make([]chan job, len(r.fixtures))
	var wg sync.WaitGroup
	for i := range queues {
		queues[i] = make(chan job, 1)
		wg.Add(1)
		go func(fi int) {
			defer wg.Done()
			for job := range queues[fi] {
				start := time.Now()
				status, err, completed := r.publish(fi, job.beta, measured)
				c.recordAt(start, job.planned, completed, status, err)
			}
		}(i)
	}
	schedule(r.ctx, duration, rate, func(i int, p time.Time) bool {
		fi := i % len(queues)
		beta := r.out.Scenario == "mixed" && (i/len(queues))%5 == 4
		select {
		case queues[fi] <- job{beta, p}:
			return true
		default:
			return false
		}
	}, c)
	for _, q := range queues {
		close(q)
	}
	wg.Wait()
}
func (r *runner) phase(duration time.Duration, measured bool) {
	get, pub := &counters{}, &counters{}
	if measured {
		get, pub = &r.out.Get, &r.out.Publish
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.gets(duration, r.out.GetRate, get) }()
	go func() { defer wg.Done(); r.publishes(duration, r.out.PublishRate, pub, measured) }()
	if r.out.GetRate == 0 && r.out.PublishRate == 0 {
		select {
		case <-r.ctx.Done():
		case <-time.After(duration):
		}
	}
	wg.Wait()
}
func (r *runner) cleanup() {
	for _, s := range r.subscribers {
		if s.client != nil {
			s.client.Stop()
		}
	}
	for _, s := range r.subscribers {
		if s.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := s.client.Wait(ctx); err != nil {
				r.out.CleanupErrors = append(r.out.CleanupErrors, "SDK shutdown")
			}
			cancel()
		}
	}
	if r.events != nil {
		close(r.collectorStop)
		<-r.collectorDone
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.ctx = cleanupCtx
	if !r.created {
		return
	}
	for _, f := range append(r.fixtures, r.read) {
		if f.key.Name == "" {
			continue
		}
		var latest state
		status, err := r.api(0, "GET", path(f.key), nil, &latest)
		if status == 404 {
			continue
		}
		if err == nil {
			f.state = latest
			_, err = r.api(0, "DELETE", path(f.key), edit(f), nil)
		}
		if err != nil {
			r.out.CleanupErrors = append(r.out.CleanupErrors, "config "+f.key.Name)
		}
	}
	for _, p := range []string{"/api/admin/namespaces/" + r.namespace + "/groups/load", "/api/admin/namespaces/" + r.namespace} {
		status, err := r.api(0, "DELETE", p, object{"confirmed": true}, nil)
		if err != nil && status != 404 {
			r.out.CleanupErrors = append(r.out.CleanupErrors, p)
		}
	}
	r.transport.CloseIdleConnections()
}
func (r *runner) evaluate() {
	r.out.Get.finish()
	r.out.Publish.finish()
	r.out.Notifications = r.book.result()
	r.out.Notifications.RecorderDropped = r.recorderDropped.Load()
	add := func(ok bool, reason string) {
		if !ok {
			r.out.Reasons = append(r.out.Reasons, reason)
		}
	}
	add(r.out.Get.failureRate() <= r.maxError, "GET errors/dropped exceed limit")
	add(r.out.Publish.failureRate() <= r.maxError, "publish errors/dropped exceed limit")
	add(r.out.Get.FromSchedule.P99 <= float64(r.getP99)/float64(time.Millisecond), "GET P99 exceeds limit")
	add(r.out.Publish.FromSchedule.P99 <= float64(r.publishP99)/float64(time.Millisecond), "publish P99 exceeds limit")
	n := r.out.Notifications
	add(n.RecorderDropped == 0, "measurement queue overflow")
	add(n.Invalid == 0, "invalid or stale SDK snapshots")
	add(n.Expected == 0 || float64(n.Unobserved)/float64(max(1, n.Expected)) <= r.maxError, "notification coverage below limit (coalesced or missing)")
	add(n.Latency.P99 <= float64(r.notificationP99)/float64(time.Millisecond), "notification P99 exceeds limit")
	add(r.out.Converged, "final SDK state did not converge")
	add(len(r.out.CleanupErrors) == 0, "cleanup failed")
	r.out.Pass = len(r.out.Reasons) == 0
}
func main() { os.Exit(run()) }
func run() (code int) {
	r := &runner{book: book{publications: map[int64]*publication{}, latest: map[int]sdk.Snapshot{}}}
	var addresses string
	flag.StringVar(&addresses, "addresses", os.Getenv("CONFHUB_LOAD_ADDRESSES"), "comma-separated instance URLs")
	flag.StringVar(&r.out.Scenario, "scenario", "fanout", "get, idle, fanout, mixed, write")
	flag.IntVar(&r.out.Clients, "clients", 1000, "real Go SDK clients (one WebSocket and subscription each)")
	flag.IntVar(&r.out.Configs, "configs", 1, "published configurations")
	flag.IntVar(&r.size, "size", 1024, "raw content bytes (32..1048576)")
	flag.IntVar(&r.out.GetRate, "get-rate", 0, "open-loop GET requests/second")
	flag.IntVar(&r.out.PublishRate, "publish-rate", 1, "open-loop publications/second")
	flag.IntVar(&r.workers, "workers", 256, "maximum concurrent GET requests")
	flag.IntVar(&r.connectWorkers, "connect-workers", 64, "parallel SDK startup workers")
	flag.DurationVar(&r.duration, "duration", 30*time.Second, "measured stage duration")
	flag.DurationVar(&r.warmup, "warmup", 5*time.Second, "unmeasured warmup")
	flag.DurationVar(&r.timeout, "timeout", 5*time.Second, "HTTP/SDK timeout")
	flag.DurationVar(&r.getP99, "get-p99", 100*time.Millisecond, "GET P99 budget from scheduled arrival")
	flag.DurationVar(&r.publishP99, "publish-p99", 500*time.Millisecond, "publication P99 budget from scheduled arrival")
	flag.DurationVar(&r.notificationP99, "notification-p99", 300*time.Millisecond, "publication start to SDK callback P99 budget")
	flag.Float64Var(&r.maxError, "max-error", .001, "maximum error/drop/unobserved ratio")
	flag.StringVar(&r.output, "output", "load.json", "JSON report")
	flag.Parse()
	if addresses == "" || os.Getenv("CONFHUB_LOAD_PASSWORD") == "" || r.size < 32 || r.size > 1<<20 || r.out.Configs < 1 || r.out.Clients < 0 || r.workers < 1 || r.connectWorkers < 1 || r.duration < time.Second || r.warmup < 0 || r.timeout <= 0 || r.out.GetRate < 0 || r.out.PublishRate < 0 || r.maxError < 0 || r.maxError > 1 {
		return 2
	}
	switch r.out.Scenario {
	case "get", "idle", "fanout", "mixed", "write":
	default:
		return 2
	}
	r.addresses = strings.Split(addresses, ",")
	r.transport = http.DefaultTransport.(*http.Transport).Clone()
	r.transport.MaxIdleConns = max(512, r.workers*2)
	r.transport.MaxIdleConnsPerHost = r.workers
	r.http = &http.Client{Transport: r.transport}
	for _, address := range r.addresses {
		u, err := url.Parse(address)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.User != nil {
			return 2
		}
		jar, _ := cookiejar.New(nil)
		r.admins = append(r.admins, &http.Client{Jar: jar, Transport: r.transport})
	}
	raw := make([]byte, 8)
	mustRand := func() { _, err := rand.Read(raw); must(err) }
	mustRand()
	r.namespace = "load-" + hex.EncodeToString(raw)
	r.out.Namespace = r.namespace
	r.out.PayloadBytes = r.size
	r.out.DurationSeconds = r.duration.Seconds()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r.ctx = ctx
	defer func() {
		_ = os.WriteFile(r.output+".phase", []byte("cleanup"), 0600)
		if err := recover(); err != nil {
			r.out.Reasons = append(r.out.Reasons, fmt.Sprint(err))
			code = 2
		}
		if r.ctx.Err() != nil {
			r.out.Reasons = append(r.out.Reasons, "interrupted")
		}
		r.cleanup()
		r.evaluate()
		data, err := json.MarshalIndent(&r.out, "", "  ")
		if err == nil {
			err = os.WriteFile(r.output, data, 0600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "report write failed")
			code = 2
		}
		fmt.Printf("scenario=%s clients=%d get=%d/s publish=%d/s pass=%t reasons=%v\n", r.out.Scenario, r.out.Clients, r.out.GetRate, r.out.PublishRate, r.out.Pass, r.out.Reasons)
		if !r.out.Pass && code == 0 {
			code = 1
		}
	}()
	r.setup()
	r.phase(r.warmup, false)
	if !r.converge(10 * time.Second) {
		panic("warmup did not converge")
	}
	start := time.Now()
	must(os.WriteFile(r.output+".phase", []byte("measure"), 0600))
	r.phase(r.duration, true)
	r.out.ElapsedSeconds = time.Since(start).Seconds()
	must(os.WriteFile(r.output+".phase", []byte("drain"), 0600))
	r.out.Converged = r.converge(10 * time.Second)
	return 0
}
