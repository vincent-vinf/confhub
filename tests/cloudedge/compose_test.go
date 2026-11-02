package cloudedge_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

type object = map[string]any
type state struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Global   int64  `json:"global_version"`
}
type mutation struct {
	State state `json:"state"`
}
type status struct {
	Mirror struct {
		Ready    bool   `json:"ready"`
		Sequence int64  `json:"sequence"`
		Owner    string `json:"sync_owner"`
	} `json:"mirror"`
	Connection struct {
		Active    bool `json:"active_sync_worker"`
		Connected bool `json:"connected"`
	} `json:"connection"`
}
type deployment struct {
	t                                                       *testing.T
	root, cloudProject, edgeProject, network, envFile, mode string
	vars                                                    map[string]string
	cloud, edges                                            []string
	admin                                                   *http.Client
	http                                                    *http.Client
}

func random() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func port(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}
func command(t *testing.T, root string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = root
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s failed: %v\n%s", args[0], err, raw)
	}
	return strings.TrimSpace(string(raw))
}
func eventually(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("timeout:", label)
}
func (d *deployment) writeEnv() {
	d.t.Helper()
	var b strings.Builder
	for k, v := range d.vars {
		fmt.Fprintf(&b, "%s=%s\n", k, v)
	}
	if err := os.WriteFile(d.envFile, []byte(b.String()), 0600); err != nil {
		d.t.Fatal(err)
	}
}
func (d *deployment) compose(cloud bool, args ...string) string {
	d.t.Helper()
	project, file := d.edgeProject, "deploy/edge/compose.yaml"
	if cloud {
		project, file = d.cloudProject, "deploy/cloud/compose.yaml"
	}
	base := []string{"docker", "compose", "--env-file", d.envFile, "-p", project, "-f", file, "--profile", "ha"}
	if d.mode == "nats" && cloud {
		base = append(base, "--profile", "nats")
	}
	return command(d.t, d.root, append(base, args...)...)
}

// Capture owned deployment state before cleanup so intermittent failures remain
// diagnosable. Never print generated credentials from Compose or application logs.
func (d *deployment) logFailure() {
	if !d.t.Failed() {
		return
	}
	redact := func(raw string) string {
		for key, value := range d.vars {
			if value != "" && (strings.Contains(key, "PASSWORD") || strings.Contains(key, "SECRET") || strings.Contains(key, "TOKEN")) {
				raw = strings.ReplaceAll(raw, value, "<REDACTED>")
			}
		}
		return raw
	}
	for _, address := range d.edges {
		code, raw := d.response(d.http, address, "GET", "/api/edge/status", nil)
		d.t.Logf("edge status %s (%d): %s", address, code, redact(string(raw)))
	}
	for _, side := range []struct{ project, file string }{{d.cloudProject, "deploy/cloud/compose.yaml"}, {d.edgeProject, "deploy/edge/compose.yaml"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		args := []string{"compose", "--env-file", d.envFile, "-p", side.project, "-f", side.file, "--profile", "ha"}
		if d.mode == "nats" && side.project == d.cloudProject {
			args = append(args, "--profile", "nats")
		}
		cmd := exec.CommandContext(ctx, "docker", append(args, "logs", "--no-color", "--tail", "60")...)
		cmd.Dir = d.root
		raw, err := cmd.CombinedOutput()
		cancel()
		d.t.Logf("deployment %s logs (%v):\n%s", side.project, err, redact(string(raw)))
	}
}
func (d *deployment) response(client *http.Client, address, method, path string, body any) (int, []byte) {
	d.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			d.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, address+path, bytes.NewReader(raw))
	if err != nil {
		d.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", address)
	res, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 9<<20))
	if err != nil {
		return 0, nil
	}
	return res.StatusCode, data
}
func (d *deployment) call(method, path string, body any, out any) {
	d.t.Helper()
	code, raw := d.response(d.admin, d.cloud[0], method, path, body)
	if code != 200 {
		d.t.Fatalf("%s %s: status %d body %s", method, path, code, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			d.t.Fatal(err)
		}
	}
}
func path(k sdk.Key) string {
	return "/api/admin/namespaces/" + url.PathEscape(k.Namespace) + "/groups/" + url.PathEscape(k.Group) + "/configs/" + url.PathEscape(k.Name)
}
func baseline(s state) object {
	return object{"expected_id": s.ID, "expected_revision": s.Revision, "confirmed": true}
}
func (d *deployment) save(k sdk.Key, s state, content, target string) state {
	d.t.Helper()
	body := baseline(s)
	body["content"] = content
	body["format"] = "text"
	if target != "" {
		body["target"] = target
	}
	var m mutation
	d.call("PUT", path(k), body, &m)
	return m.State
}
func (d *deployment) read(k sdk.Key) state {
	d.t.Helper()
	var s state
	d.call("GET", path(k), nil, &s)
	return s
}
func (d *deployment) copy(k sdk.Key, s state, op string, source int64) state {
	d.t.Helper()
	body := baseline(s)
	if source > 0 {
		body["source_version"] = source
	}
	var m mutation
	d.call("POST", path(k)+"/"+op, body, &m)
	return m.State
}
func rules(enabled bool) []object {
	return []object{
		{"id": "ip", "name": "IP", "enabled": enabled, "conditions": []object{{"tag": "sys.ip", "operator": "ip_range", "values": []string{"192.168.2.1", "192.168.2.5"}}, {"tag": "env", "operator": "eq", "values": []string{"test"}}}},
		{"id": "env", "name": "env", "enabled": enabled, "conditions": []object{{"tag": "env", "operator": "eq", "values": []string{"test"}}}},
	}
}
func (d *deployment) setRules(k sdk.Key, s state, rs []object, source int64) state {
	d.t.Helper()
	body := baseline(s)
	body["rules"] = rs
	if source > 0 {
		body["source_version"] = source
	}
	var m mutation
	d.call("PUT", path(k)+"/rules", body, &m)
	return m.State
}
func (d *deployment) edgeStatus(i int) status {
	d.t.Helper()
	var s status
	code, raw := d.response(d.http, d.edges[i], "GET", "/api/edge/status", nil)
	if code == 200 {
		if err := json.Unmarshal(raw, &s); err != nil {
			d.t.Fatal(err)
		}
	}
	return s
}
func (d *deployment) ready() {
	d.t.Helper()
	for _, address := range append(append([]string{}, d.cloud...), d.edges...) {
		eventually(d.t, "ready "+address, func() bool { code, _ := d.response(d.http, address, "GET", "/health/ready", nil); return code == 200 })
	}
}
func (d *deployment) get(address string, k sdk.Key, tags map[string]string) (sdk.Snapshot, int) {
	query := url.Values{"namespace": {k.Namespace}, "group": {k.Group}, "name": {k.Name}}
	raw, _ := json.Marshal(tags)
	query.Set("tags", string(raw))
	code, data := d.response(d.http, address, "GET", "/api/client/config?"+query.Encode(), nil)
	var s sdk.Snapshot
	if code == 200 {
		if err := json.Unmarshal(data, &s); err != nil {
			d.t.Fatal(err)
		}
	}
	return s, code
}
func (d *deployment) expect(k sdk.Key, content string, version int64, rule string) {
	d.t.Helper()
	for _, address := range d.edges {
		eventually(d.t, "mirror "+k.Name, func() bool {
			got, code := d.get(address, k, map[string]string{"env": "test", "sys.ip": "192.168.2.3"})
			return code == 200 && got.Content == content && got.Version == version && got.RuleID == rule
		})
	}
}
func (d *deployment) client(addresses []string, tags map[string]string) *sdk.Client {
	d.t.Helper()
	c, err := sdk.New(sdk.Options{Addresses: addresses, Tags: tags, Timeout: time.Second})
	if err != nil {
		d.t.Fatal(err)
	}
	d.t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := c.Close(ctx); err != nil {
			d.t.Error(err)
		}
	})
	return c
}

type observer struct {
	mu       sync.Mutex
	latest   sdk.Snapshot
	backward bool
}

func watch(t *testing.T, c *sdk.Client, k sdk.Key) *observer {
	t.Helper()
	o := &observer{}
	if err := c.Subscribe(context.Background(), k, func(_ context.Context, s sdk.Snapshot) {
		o.mu.Lock()
		defer o.mu.Unlock()
		if s.Sequence < o.latest.Sequence {
			o.backward = true
		}
		o.latest = s
	}); err != nil {
		t.Fatal(err)
	}
	return o
}
func (o *observer) expect(t *testing.T, content string, version int64, rule string) {
	t.Helper()
	eventually(t, "SDK callback", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return !o.backward && o.latest.Content == content && o.latest.Version == version && o.latest.RuleID == rule && !o.latest.Deleted
	})
}

func TestCloudEdgeCompose(t *testing.T) {
	if os.Getenv("CONFHUB_RUN_CLOUD_EDGE") != "1" {
		t.Skip("run make test-cloud-edge to deploy two owned Compose projects")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	image := os.Getenv("CONFHUB_EDGE_TEST_IMAGE")
	if image == "" {
		image = "confhub:cloud-edge-test"
		command(t, root, "docker", "build", "-t", image, ".")
	}
	for _, mode := range []string{"websocket", "nats"} {
		t.Run(mode, func(t *testing.T) {
			id := "confhub-edge-test-" + random()[:10]
			jar, _ := cookiejar.New(nil)
			d := &deployment{t: t, root: root, cloudProject: id + "-cloud", edgeProject: id + "-edge", network: id + "-mesh", mode: mode, envFile: filepath.Join(t.TempDir(), "compose.env"), admin: &http.Client{Jar: jar, Timeout: 5 * time.Second}, http: &http.Client{Timeout: time.Second}}
			d.vars = map[string]string{"CONFHUB_IMAGE": image, "CONFHUB_DB_PASSWORD": random(), "CONFHUB_ADMIN_PASSWORD": random(), "CONFHUB_JWT_SECRET": random() + random(), "CONFHUB_REPLICATION_TOKEN": random() + random(), "CONFHUB_NATS_PASSWORD": random(), "CONFHUB_REPLICATION_NETWORK": d.network, "CONFHUB_CLOUD_ALIAS": id + "-cloud", "CONFHUB_NATS_ALIAS": id + "-nats", "CONFHUB_REPLICATION_TRANSPORT": mode, "CONFHUB_REPLICATION_LEASE": "3s", "CONFHUB_EVENT_RETENTION": "2s", "CONFHUB_CLEANUP_INTERVAL": "1s", "CONFHUB_CLOUD_PORT": port(t), "CONFHUB_CLOUD_PORT2": port(t), "CONFHUB_EDGE_PORT": port(t), "CONFHUB_EDGE_PORT2": port(t)}
			if mode == "nats" {
				d.vars["CONFHUB_CLOUD_REPLICATION_URL"] = "nats://confhub:" + d.vars["CONFHUB_NATS_PASSWORD"] + "@" + id + "-nats:4222"
				d.vars["CONFHUB_EDGE_REPLICATION_URL"] = d.vars["CONFHUB_CLOUD_REPLICATION_URL"]
			} else {
				d.vars["CONFHUB_EDGE_REPLICATION_URL"] = "ws://" + id + "-cloud:8080/api/replication/watch"
			}
			d.cloud = []string{"http://127.0.0.1:" + d.vars["CONFHUB_CLOUD_PORT"], "http://127.0.0.1:" + d.vars["CONFHUB_CLOUD_PORT2"]}
			d.edges = []string{"http://127.0.0.1:" + d.vars["CONFHUB_EDGE_PORT"], "http://127.0.0.1:" + d.vars["CONFHUB_EDGE_PORT2"]}
			d.writeEnv()
			command(t, root, "docker", "network", "create", d.network)
			t.Cleanup(func() {
				d.logFailure()
				d.compose(false, "down", "--volumes", "--remove-orphans")
				d.compose(true, "down", "--volumes", "--remove-orphans")
				command(t, root, "docker", "network", "rm", d.network)
			})
			d.compose(true, "up", "-d")
			for _, address := range d.cloud {
				eventually(t, "cloud ready", func() bool { code, _ := d.response(d.http, address, "GET", "/health/ready", nil); return code == 200 })
			}
			d.call("POST", "/api/admin/login", object{"username": "admin", "password": d.vars["CONFHUB_ADMIN_PASSWORD"]}, nil)
			main := sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "main"}
			s := d.save(main, state{}, "main one", "")
			for i := 0; i < 20; i++ {
				d.save(sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: fmt.Sprintf("all-%02d", i)}, state{}, fmt.Sprintf("config-%d", i), "")
			}
			large := sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "large"}
			largeContent := strings.Repeat("\t<>&\n", 50000)
			d.save(large, state{}, largeContent, "")
			token := d.vars["CONFHUB_REPLICATION_TOKEN"]
			d.vars["CONFHUB_REPLICATION_TOKEN"] = random() + random()
			d.writeEnv()
			d.compose(false, "up", "-d")
			scene := func(name string, fn func(*testing.T)) {
				ok := t.Run(name, func(st *testing.T) { d.t = st; defer func() { d.t = t }(); fn(st) })
				if !ok {
					t.FailNow()
				}
			}
			scene("authentication_and_initial_readiness", func(t *testing.T) {
				for i, address := range d.edges {
					eventually(t, "edge status", func() bool {
						code, _ := d.response(d.http, address, "GET", "/api/edge/status", nil)
						return code == 200
					})
					code, _ := d.response(d.http, address, "GET", "/health/ready", nil)
					if code != 503 || d.edgeStatus(i).Mirror.Ready {
						t.Fatal("uninitialized edge served config")
					}
				}
				time.Sleep(time.Second)
				if d.edgeStatus(0).Mirror.Ready {
					t.Fatal("wrong token allowed replication")
				}
				d.vars["CONFHUB_REPLICATION_TOKEN"] = token
				d.writeEnv()
				d.compose(false, "up", "-d", "--force-recreate", "app", "app2")
				d.ready()
				for _, address := range d.edges {
					jar, _ := cookiejar.New(nil)
					admin := &http.Client{Jar: jar, Timeout: 5 * time.Second}
					code, raw := d.response(admin, address, "POST", "/api/admin/login", object{"username": "admin", "password": d.vars["CONFHUB_ADMIN_PASSWORD"]})
					if code != 200 {
						t.Fatal("edge login", code, string(raw))
					}
					code, raw = d.response(admin, address, "GET", "/api/admin/me", nil)
					var me struct {
						ReadOnly bool   `json:"read_only"`
						Role     string `json:"role"`
					}
					if err := json.Unmarshal(raw, &me); err != nil || code != 200 || !me.ReadOnly || me.Role != "edge" {
						t.Fatal("edge identity", code, string(raw), err)
					}
					code, raw = d.response(admin, address, "GET", path(main), nil)
					if code != 200 {
						t.Fatal("edge mirror read", code, string(raw))
					}
					code, raw = d.response(admin, address, "PUT", path(main), object{"confirmed": true, "content": "forbidden"})
					if code != 403 {
						t.Fatal("edge write allowed", code, string(raw))
					}
					for _, method := range []string{"GET", "PUT", "POST", "DELETE"} {
						code, _ := d.response(d.http, address, method, "/api/admin/namespaces", nil)
						expected := 401
						if method != "GET" {
							expected = 403
						}
						if code != expected {
							t.Fatal("edge exposed management", method, code)
						}
					}
				}
			})
			scene("full_snapshot_all_configs_and_large_chunks", func(t *testing.T) {
				d.expect(main, "main one", 1, "")
				d.expect(large, largeContent, 1, "")
				for i := 0; i < 20; i++ {
					d.expect(sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: fmt.Sprintf("all-%02d", i)}, fmt.Sprintf("config-%d", i), 1, "")
				}
			})
			normal := d.client([]string{d.edges[0], d.edges[1]}, nil)
			gray := d.client([]string{d.edges[1], d.edges[0]}, map[string]string{"env": "test", "sys.ip": "192.168.2.3"})
			no, goGray := watch(t, normal, main), watch(t, gray, main)
			python := newPython(t, d, main)
			defer python.close()
			scene("main_beta_ordered_tags_and_rollback", func(t *testing.T) {
				no.expect(t, "main one", 1, "")
				s = d.setRules(main, s, rules(true), 1)
				s = d.save(main, s, "beta one", "beta")
				d.expect(main, "beta one", 0, "ip")
				goGray.expect(t, "beta one", 0, "ip")
				python.expect("beta one", 0, "ip")
				s = d.save(main, s, "beta two", "beta")
				d.expect(main, "beta two", 0, "ip")
				goGray.expect(t, "beta two", 0, "ip")
				no.expect(t, "main one", 1, "")
				rs := rules(true)
				rs[0], rs[1] = rs[1], rs[0]
				s = d.setRules(main, s, rs, 0)
				d.expect(main, "beta two", 0, "env")
				s = d.setRules(main, s, rules(false), 0)
				d.expect(main, "main one", 1, "")
				goGray.expect(t, "main one", 1, "")
				python.expect("main one", 1, "")
				s = d.save(main, s, "main two", "")
				no.expect(t, "main two", 2, "")
				s = d.setRules(main, s, rules(true), 0)
				s = d.copy(main, s, "rollback", 1)
				d.expect(main, "beta two", 0, "ip")
				no.expect(t, "main one", 3, "")
				s = d.copy(main, s, "promote", 0)
				no.expect(t, "beta two", 4, "")
				s = d.setRules(main, s, []object{}, 0)
				d.expect(main, "beta two", 4, "")
				goGray.expect(t, "beta two", 4, "")
				python.expect("beta two", 4, "")
			})
			scene("concurrent_publications_and_many_subscribers", func(t *testing.T) {
				key := sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "concurrency"}
				d.save(key, state{}, "start", "")
				d.expect(key, "start", 1, "")
				var observers []*observer
				for i := 0; i < 32; i++ {
					c := d.client([]string{d.edges[i%2]}, nil)
					observers = append(observers, watch(t, c, key))
				}
				var wg sync.WaitGroup
				success := make(chan int, 32)
				current := d.read(key)
				for i := 0; i < 32; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						body := baseline(current)
						body["content"] = fmt.Sprintf("writer-%d", i)
						body["format"] = "text"
						code, _ := d.response(d.admin, d.cloud[i%2], "PUT", path(key), body)
						success <- code
					}(i)
				}
				wg.Wait()
				close(success)
				winners := 0
				for code := range success {
					if code == 200 {
						winners++
					} else if code != 409 {
						t.Fatal("unexpected writer result", code)
					}
				}
				if winners != 1 {
					t.Fatal("optimistic lock winners", winners)
				}
				state := d.read(key)
				state = d.save(key, state, "final", "")
				if state.Global != 3 {
					t.Fatal("wrong version after conflict", state)
				}
				d.expect(key, "final", 3, "")
				for _, o := range observers {
					o.expect(t, "final", 3, "")
				}
			})
			scene("batch_move_delete_and_identity_recreation", func(t *testing.T) {
				d.call("POST", "/api/admin/namespaces", object{"name": "moved"}, nil)
				d.call("POST", "/api/admin/namespaces/moved/groups", object{"name": "target"}, nil)
				old := sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "moving"}
				moving := d.save(old, state{}, "move me", "")
				d.expect(old, "move me", 1, "")
				d.call("POST", "/api/admin/configs/batch-move", object{"confirmed": true, "namespace": "moved", "group": "target", "configs": []object{{"key": old, "expected_id": moving.ID, "expected_revision": moving.Revision}}}, nil)
				newer := sdk.Key{Namespace: "moved", Group: "target", Name: "moving"}
				d.expect(newer, "move me", 1, "")
				eventually(t, "old key deleted", func() bool { _, code := d.get(d.edges[0], old, nil); return code == 404 })
				got, _ := d.get(d.edges[0], newer, nil)
				if got.ID != moving.ID {
					t.Fatal("move changed identity")
				}
				moving = d.read(newer)
				d.call("POST", "/api/admin/configs/batch-delete", object{"confirmed": true, "configs": []object{{"key": newer, "expected_id": moving.ID, "expected_revision": moving.Revision}}}, nil)
				eventually(t, "batch deleted", func() bool { _, code := d.get(d.edges[1], newer, nil); return code == 404 })
				rebuilt := d.save(newer, state{}, "rebuilt", "")
				d.expect(newer, "rebuilt", 1, "")
				if rebuilt.ID == moving.ID {
					t.Fatal("recreated config retained identity")
				}
			})
			scene("sync_worker_takeover_and_client_failover", func(t *testing.T) {
				i := 0
				if d.edgeStatus(1).Connection.Active {
					i = 1
				}
				survivor := 1 - i
				service := []string{"app", "app2"}[i]
				container := d.compose(false, "ps", "-q", service)
				command(t, root, "docker", "update", "--restart=no", container)
				d.compose(false, "kill", "-s", "SIGKILL", service)
				// Prevent restart:unless-stopped from immediately relaunching the killed owner.
				d.compose(false, "stop", service)
				eventually(t, "standby acquired lease", func() bool {
					return d.edgeStatus(survivor).Connection.Active && d.edgeStatus(survivor).Connection.Connected
				})
				s = d.save(main, s, "takeover", "")
				no.expect(t, "takeover", 5, "")
				goGray.expect(t, "takeover", 5, "")
				python.expect("takeover", 5, "")
				d.compose(false, "up", "-d", "--force-recreate", service)
				d.ready()
				d.expect(main, "takeover", 5, "")
			})
			scene("cloud_outage_restart_and_expired_log_compensation", func(t *testing.T) {
				ghost := sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "ghost"}
				gs := d.save(ghost, state{}, "last known", "")
				d.expect(ghost, "last known", 1, "")
				containers := []string{d.compose(true, "ps", "-q", "app"), d.compose(true, "ps", "-q", "app2")}
				if mode == "nats" {
					containers = []string{d.compose(true, "ps", "-q", "nats")}
				}
				for _, id := range containers {
					command(t, root, "docker", "network", "disconnect", d.network, id)
				}
				eventually(t, "sync disconnected", func() bool { return !d.edgeStatus(0).Connection.Connected && !d.edgeStatus(1).Connection.Connected })
				for _, address := range d.edges {
					code, _ := d.response(d.http, address, "GET", "/health/ready", nil)
					if code != 200 {
						t.Fatal("cloud outage made edge unready")
					}
				}
				d.call("DELETE", path(ghost), baseline(gs), nil)
				s = d.save(main, s, "after recovery", "")
				d.compose(false, "restart", "app", "app2")
				d.ready()
				d.expect(main, "takeover", 5, "")
				for _, address := range d.edges {
					jar, _ := cookiejar.New(nil)
					admin := &http.Client{Jar: jar, Timeout: 5 * time.Second}
					code, raw := d.response(admin, address, "POST", "/api/admin/login", object{"username": "admin", "password": d.vars["CONFHUB_ADMIN_PASSWORD"]})
					if code != 200 {
						t.Fatal("offline edge login", code, string(raw))
					}
					code, raw = d.response(admin, address, "GET", path(main), nil)
					if code != 200 {
						t.Fatal("offline edge console read", code, string(raw))
					}
				}
				fresh := d.client([]string{d.edges[0]}, nil)
				got, err := fresh.Get(context.Background(), ghost)
				if err != nil || got.Content != "last known" || got.Source != sdk.Online {
					t.Fatal("offline edge cannot serve new client", got, err)
				}
				time.Sleep(4 * time.Second) // Cloud's test-only retention is 2s.
				for _, id := range containers {
					alias := d.vars["CONFHUB_CLOUD_ALIAS"]
					if mode == "nats" {
						alias = d.vars["CONFHUB_NATS_ALIAS"]
					}
					command(t, root, "docker", "network", "connect", "--alias", alias, d.network, id)
				}
				d.expect(main, "after recovery", 6, "")
				no.expect(t, "after recovery", 6, "")
				goGray.expect(t, "after recovery", 6, "")
				python.expect("after recovery", 6, "")
				for _, address := range d.edges {
					eventually(t, "full replacement removed ghost", func() bool { _, code := d.get(address, ghost, nil); return code == 404 })
				}
			})
			if mode == "nats" {
				scene("broker_restart_preserves_offline_service_and_resumes_updates", func(t *testing.T) {
					d.compose(true, "stop", "-t", "0", "nats")
					eventually(t, "broker outage detected", func() bool {
						return !d.edgeStatus(0).Connection.Connected && !d.edgeStatus(1).Connection.Connected
					})
					s = d.save(main, s, "published during broker outage", "")
					s = d.setRules(main, s, rules(true), s.Global)
					s = d.save(main, s, "beta during broker outage", "beta")
					created := sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "created-offline"}
					d.save(created, state{}, "created during broker outage", "")
					d.call("DELETE", path(large), baseline(d.read(large)), nil)
					d.ready()
					d.expect(main, "after recovery", 6, "")
					d.expect(large, largeContent, 1, "")
					for _, address := range d.edges {
						if _, code := d.get(address, created, nil); code != 404 {
							t.Fatal("offline edge exposed unsynchronized config", code)
						}
					}
					started := time.Now()
					d.compose(true, "start", "nats")
					d.expect(main, "beta during broker outage", 0, "ip")
					no.expect(t, "published during broker outage", 7, "")
					goGray.expect(t, "beta during broker outage", 0, "ip")
					python.expect("beta during broker outage", 0, "ip")
					d.expect(created, "created during broker outage", 1, "")
					for _, address := range d.edges {
						eventually(t, "broker recovery removed deleted config", func() bool { _, code := d.get(address, large, nil); return code == 404 })
					}
					t.Logf("broker restart to mirrored state and Go/Python SDK convergence: %s", time.Since(started))
					s = d.setRules(main, s, rules(false), 0)
					d.expect(main, "published during broker outage", 7, "")
					goGray.expect(t, "published during broker outage", 7, "")
					python.expect("published during broker outage", 7, "")
					s = d.save(main, s, "live after broker restart", "")
					d.expect(main, "live after broker restart", 8, "")
					no.expect(t, "live after broker restart", 8, "")
					goGray.expect(t, "live after broker restart", 8, "")
					python.expect("live after broker restart", 8, "")
				})
			}
		})
	}
}

type pythonBridge struct {
	t     *testing.T
	cmd   *exec.Cmd
	input io.WriteCloser
	lines *bufio.Scanner
	mutex sync.Mutex
}

func newPython(t *testing.T, d *deployment, key sdk.Key) *pythonBridge {
	t.Helper()
	binary := filepath.Join(d.root, "sdk/python/.venv/bin/python")
	cmd := exec.Command(binary, filepath.Join(d.root, "tests/python_worker.py"))
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 65536), 8<<20)
	p := &pythonBridge{t: t, cmd: cmd, input: in, lines: scanner}
	p.call(object{"op": "init", "addresses": d.edges, "tags": object{"env": "test", "sys.ip": "192.168.2.3"}})
	p.call(object{"op": "subscribe", "key": key})
	return p
}
func (p *pythonBridge) call(request object) object {
	p.t.Helper()
	p.mutex.Lock()
	defer p.mutex.Unlock()
	raw, _ := json.Marshal(request)
	if _, err := p.input.Write(append(raw, '\n')); err != nil {
		p.t.Fatal(err)
	}
	result := make(chan object, 1)
	go func() {
		if !p.lines.Scan() {
			result <- nil
			return
		}
		var response object
		if json.Unmarshal(p.lines.Bytes(), &response) != nil {
			result <- nil
			return
		}
		result <- response
	}()
	select {
	case response := <-result:
		if response == nil || response["error"] != nil {
			p.t.Fatal("Python SDK command failed", response)
		}
		return response["result"].(map[string]any)
	case <-time.After(10 * time.Second):
		p.cmd.Process.Kill()
		p.t.Fatal("Python SDK timed out")
		return nil
	}
}
func (p *pythonBridge) expect(content string, version int64, rule string) {
	p.t.Helper()
	eventually(p.t, "Python SDK callback", func() bool {
		response := p.call(object{"op": "latest", "key": sdk.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "main"}})
		value, ok := response["value"].(map[string]any)
		if !ok {
			return false
		}
		id, _ := value["rule_id"].(string)
		return value["content"] == content && value["version"] == float64(version) && id == rule
	})
}
func (p *pythonBridge) close() {
	p.call(object{"op": "close"})
	p.input.Close()
	if err := p.cmd.Wait(); err != nil {
		p.t.Error(err)
	}
}
