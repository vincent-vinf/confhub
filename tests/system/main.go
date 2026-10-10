// Command system tests a running cluster exclusively through public APIs and SDKs.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "gitlab.bodesitech.com/bodesi/confhub/sdk/go"
)

type object = map[string]any
type version struct {
	Number      int64  `json:"number"`
	Content     string `json:"content"`
	Format      string `json:"format"`
	Description string `json:"description"`
	Source      int64  `json:"source_version"`
}
type condition struct {
	Tag      string   `json:"tag"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}
type rule struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	Conditions []condition `json:"conditions"`
}
type beta struct {
	Content     string `json:"content"`
	Format      string `json:"format"`
	Description string `json:"description"`
}
type state struct {
	ID       string            `json:"id"`
	Revision int64             `json:"revision"`
	Last     int64             `json:"last_version"`
	Global   int64             `json:"global_version"`
	Versions map[int64]version `json:"versions"`
	Rules    []rule            `json:"rules"`
	Beta     *beta             `json:"beta"`
}
type mutation struct {
	State    state `json:"state"`
	Changed  bool  `json:"changed"`
	Sequence int64 `json:"sequence"`
}
type result struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}
type report struct {
	Version   string   `json:"version"`
	Mode      string   `json:"mode"`
	Namespace string   `json:"namespace"`
	Instances int      `json:"instances"`
	Writers   int      `json:"writers"`
	Clients   int      `json:"clients"`
	Configs   int      `json:"configs"`
	Rounds    int      `json:"rounds"`
	Seed      int64    `json:"seed"`
	Results   []result `json:"results"`
}
type runner struct {
	ctx                                           context.Context
	addresses                                     []string
	admins                                        []*http.Client
	namespace                                     string
	created                                       bool
	groups                                        []string
	keys                                          []sdk.Key
	timeout                                       time.Duration
	writers, clients, configs, rounds             int
	seed                                          int64
	python, worker, control, controlToken, output string
	report                                        report
	mu                                            sync.Mutex
}

func require(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Errorf(format, args...))
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func decode(raw []byte, value any) { must(json.Unmarshal(raw, value)) }
func (r *runner) request(index int, method, path string, body any) (int, []byte, error) {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, r.addresses[index%len(r.addresses)]+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", r.addresses[index%len(r.addresses)])
	res, err := r.admins[index%len(r.admins)].Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s transport failed", method, path)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 9<<20))
	return res.StatusCode, data, err
}
func (r *runner) call(index int, method, path string, body any, out any) {
	status, raw, err := r.request(index, method, path, body)
	must(err)
	require(status == 200, "%s %s expected 200, got %d", method, path, status)
	if out != nil {
		decode(raw, out)
	}
}
func (r *runner) status(index int, method, path string, body any, want int) {
	got, _, err := r.request(index, method, path, body)
	must(err)
	require(got == want, "%s %s expected %d, got %d", method, path, want, got)
}
func (r *runner) key(name string) sdk.Key {
	k := sdk.Key{Namespace: r.namespace, Group: "cases", Name: name}
	r.mu.Lock()
	r.keys = append(r.keys, k)
	r.mu.Unlock()
	return k
}
func configPath(k sdk.Key) string {
	return "/api/admin/namespaces/" + url.PathEscape(k.Namespace) + "/groups/" + url.PathEscape(k.Group) + "/configs/" + url.PathEscape(k.Name)
}
func baseline(s state) object {
	return object{"expected_id": s.ID, "expected_revision": s.Revision, "confirmed": true}
}
func editBody(s state, content, target string) object {
	b := baseline(s)
	b["content"] = content
	b["format"] = "text"
	b["description"] = "system test"
	if target != "" {
		b["target"] = target
	}
	return b
}
func (r *runner) save(index int, k sdk.Key, s state, content, target string) mutation {
	var m mutation
	r.call(index, "PUT", configPath(k), editBody(s, content, target), &m)
	return m
}
func (r *runner) read(k sdk.Key) state {
	var s state
	r.call(0, "GET", configPath(k), nil, &s)
	return s
}
func (r *runner) rules(k sdk.Key, s state, rules []rule, source int64) state {
	b := baseline(s)
	b["rules"] = rules
	b["source_version"] = source
	var m mutation
	r.call(0, "PUT", configPath(k)+"/rules", b, &m)
	return m.State
}
func (r *runner) copy(k sdk.Key, s state, action string, source int64) state {
	b := baseline(s)
	if source > 0 {
		b["source_version"] = source
	}
	var m mutation
	r.call(0, "POST", configPath(k)+"/"+action, b, &m)
	return m.State
}
func grayRules() []rule {
	return []rule{{ID: "specific", Name: "IP and env", Enabled: true, Conditions: []condition{{Tag: "env", Operator: "in", Values: []string{"gray", "canary"}}, {Tag: "sys.ip", Operator: "ip_range", Values: []string{"192.168.2.1", "192.168.2.5"}}}}, {ID: "fallback", Name: "env", Enabled: true, Conditions: []condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}}
}
func (r *runner) wait(label string, fn func() bool) {
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-r.ctx.Done():
			panic(r.ctx.Err())
		case <-timer.C:
			panic(fmt.Errorf("timeout: %s", label))
		case <-ticker.C:
		}
	}
}
func closeSDK(c *sdk.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	must(c.Close(ctx))
}
func (r *runner) sdk(addresses []string, tags map[string]string, cache string) *sdk.Client {
	c, err := sdk.New(sdk.Options{Addresses: addresses, Tags: tags, CacheDir: cache, Timeout: time.Second})
	must(err)
	return c
}

type observer struct {
	mu     sync.Mutex
	latest map[sdk.Key]sdk.Snapshot
	count  map[sdk.Key]int
	error  string
}

func newObserver() *observer {
	return &observer{latest: map[sdk.Key]sdk.Snapshot{}, count: map[sdk.Key]int{}}
}
func (o *observer) callback(_ context.Context, s sdk.Snapshot) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if previous, ok := o.latest[s.Key]; ok && previous.Sequence > s.Sequence {
		o.error = "older snapshot replaced newer state"
	}
	o.latest[s.Key] = s
	o.count[s.Key]++
}
func (o *observer) value(k sdk.Key) (sdk.Snapshot, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	require(o.error == "", "%s", o.error)
	return o.latest[k], o.count[k]
}
func matches(s sdk.Snapshot, id, content, ruleID string, v int64, deleted bool) bool {
	return s.ID == id && s.Content == content && s.RuleID == ruleID && s.Version == v && s.Beta == (ruleID != "") && s.Deleted == deleted && (deleted || s.Format == "text")
}
func (r *runner) observed(o *observer, k sdk.Key, s state, content, ruleID string, v int64) {
	r.wait("SDK callback "+k.Name, func() bool { got, _ := o.value(k); return matches(got, s.ID, content, ruleID, v, false) })
}
func (r *runner) get(c *sdk.Client, k sdk.Key, s state, content, ruleID string, v int64) {
	r.wait("SDK GET "+k.Name, func() bool {
		got, err := c.Get(r.ctx, k)
		return err == nil && matches(got, s.ID, content, ruleID, v, false) && got.Source == sdk.Online
	})
}
func (r *runner) unchanged(o *observer, k sdk.Key, count int) {
	timer := time.NewTimer(350 * time.Millisecond)
	defer timer.Stop()
	for {
		_, got := o.value(k)
		require(got == count, "unexpected callback on unaffected client")
		select {
		case <-timer.C:
			return
		case <-r.ctx.Done():
			panic(r.ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (r *runner) scene(name string, fn func()) {
	start := time.Now()
	res := result{Name: name}
	func() {
		defer func() {
			if e := recover(); e != nil {
				res.Error = fmt.Sprint(e)
			}
		}()
		fn()
	}()
	res.Seconds = time.Since(start).Seconds()
	r.report.Results = append(r.report.Results, res)
	if res.Error != "" {
		fmt.Printf("FAIL %s: %s\n", name, res.Error)
	} else {
		fmt.Printf("PASS %s (%.2fs)\n", name, res.Seconds)
	}
	must(r.writeReport())
}
func (r *runner) writeReport() error {
	if err := os.MkdirAll(r.output, 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(r.report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(r.output, "system.json"), raw, 0600); err != nil {
		return err
	}
	type failure struct {
		Message string `xml:"message,attr"`
	}
	type testcase struct {
		Name    string   `xml:"name,attr"`
		Time    float64  `xml:"time,attr"`
		Failure *failure `xml:"failure,omitempty"`
	}
	type suite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Cases    []testcase `xml:"testcase"`
	}
	s := suite{Name: "ConfHub system", Tests: len(r.report.Results)}
	for _, res := range r.report.Results {
		c := testcase{Name: res.Name, Time: res.Seconds}
		if res.Error != "" {
			s.Failures++
			c.Failure = &failure{Message: res.Error}
		}
		s.Cases = append(s.Cases, c)
	}
	raw, err = xml.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.output, "system.xml"), append([]byte(xml.Header), raw...), 0600)
}
func (r *runner) cleanup() {
	for _, k := range r.keys {
		status, raw, err := r.request(0, "GET", configPath(k), nil)
		must(err)
		if status == 404 {
			continue
		}
		require(status == 200, "cleanup read failed: %s HTTP %d", k.Name, status)
		var s state
		decode(raw, &s)
		r.status(0, "DELETE", configPath(k), baseline(s), 200)
	}
	if r.created {
		for _, g := range r.groups {
			r.status(0, "DELETE", "/api/admin/namespaces/"+r.namespace+"/groups/"+g, object{"confirmed": true}, 200)
		}
		r.status(0, "DELETE", "/api/admin/namespaces/"+r.namespace, object{"confirmed": true}, 200)
	}
}
func main() { os.Exit(run()) }
func run() (exitCode int) {
	r := &runner{}
	var addresses, scenes string
	flag.StringVar(&addresses, "addresses", os.Getenv("CONFHUB_SYSTEM_ADDRESSES"), "comma-separated running instance URLs (at least two)")
	flag.StringVar(&scenes, "scenarios", "business,protocol,concurrency,random,presence", "comma-separated suite names; faults requires isolated controller")
	flag.IntVar(&r.writers, "writers", 32, "concurrent editors")
	flag.IntVar(&r.clients, "clients", 100, "subscribing clients")
	flag.IntVar(&r.configs, "configs", 10, "distinct configurations (1-10)")
	flag.IntVar(&r.rounds, "rounds", 10, "concurrency rounds")
	flag.Int64Var(&r.seed, "seed", 20261010, "reproducible random seed")
	flag.DurationVar(&r.timeout, "timeout", 20*time.Second, "per-request/condition timeout")
	flag.StringVar(&r.python, "python", "../../sdk/python/.venv/bin/python", "Python interpreter with installed SDK")
	flag.StringVar(&r.worker, "python-worker", "../python_worker.py", "Python SDK worker")
	flag.StringVar(&r.output, "report-dir", "../../test-results/system", "report directory")
	flag.Parse()
	r.addresses = strings.Split(addresses, ",")
	if addresses == "" || len(r.addresses) < 2 || r.writers < 2 || r.clients < 1 || r.configs < 1 || r.configs > 10 || r.rounds < 1 || r.timeout <= 0 || os.Getenv("CONFHUB_SYSTEM_PASSWORD") == "" {
		fmt.Fprintln(os.Stderr, "provide at least two addresses, valid sizes, and CONFHUB_SYSTEM_PASSWORD")
		return 2
	}
	for i, a := range r.addresses {
		u, e := url.Parse(a)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			fmt.Fprintln(os.Stderr, "invalid instance URL")
			return 2
		}
		r.addresses[i] = strings.TrimRight(a, "/")
		jar, _ := cookiejar.New(nil)
		r.admins = append(r.admins, &http.Client{Jar: jar, Timeout: r.timeout})
	}
	r.control = os.Getenv("CONFHUB_SYSTEM_CONTROL")
	r.controlToken = os.Getenv("CONFHUB_SYSTEM_CONTROL_TOKEN")
	selected := strings.Split(scenes, ",")
	for _, name := range selected {
		if name != "business" && name != "protocol" && name != "concurrency" && name != "random" && name != "presence" && name != "faults" {
			fmt.Fprintln(os.Stderr, "unknown scenario", name)
			return 2
		}
		if name == "faults" && (r.control == "" || r.controlToken == "" || len(r.addresses) < 3) {
			fmt.Fprintln(os.Stderr, "faults requires isolated controller and three instances")
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r.ctx = ctx
	entropy := make([]byte, 8)
	if _, e := rand.Read(entropy); e != nil {
		fmt.Fprintln(os.Stderr, "cannot generate run ID")
		return 2
	}
	r.namespace = "system-" + hex.EncodeToString(entropy)
	revision, _ := exec.Command("git", "rev-parse", "HEAD").Output()
	mode := "connected"
	if r.control != "" {
		mode = "isolated"
	}
	r.report = report{Version: strings.TrimSpace(string(revision)), Mode: mode, Namespace: r.namespace, Instances: len(r.addresses), Writers: r.writers, Clients: r.clients, Configs: r.configs, Rounds: r.rounds, Seed: r.seed}
	defer func() {
		if e := recover(); e != nil {
			fmt.Fprintln(os.Stderr, "runner failed:", e)
			exitCode = 1
		}
	}()
	r.scene("setup", func() {
		for i := range r.addresses {
			r.status(i, "GET", "/health/ready", nil, 200)
			r.status(i, "POST", "/api/admin/login", object{"username": "admin", "password": os.Getenv("CONFHUB_SYSTEM_PASSWORD")}, 200)
		}
		r.call(0, "POST", "/api/admin/namespaces", object{"name": r.namespace}, nil)
		r.created = true
		for _, g := range []string{"cases", "other"} {
			r.call(0, "POST", "/api/admin/namespaces/"+r.namespace+"/groups", object{"name": g}, nil)
			r.groups = append(r.groups, g)
		}
	})
	if r.report.Results[0].Error == "" {
		for _, name := range selected {
			if ctx.Err() != nil {
				break
			}
			r.scene(name, func() {
				switch name {
				case "business":
					r.business()
				case "protocol":
					r.protocol()
				case "concurrency":
					r.concurrency()
				case "random":
					r.random()
				case "presence":
					r.presence()
				case "faults":
					r.faults()
				}
			})
		}
	}
	// Cleanup is independently reported even after cancellation or a failed scene.
	r.ctx = context.Background()
	r.scene("cleanup", r.cleanup)
	for _, res := range r.report.Results {
		if res.Error != "" {
			return 1
		}
	}
	if ctx.Err() != nil {
		return 1
	}
	fmt.Println("reports:", r.output)
	return 0
}
