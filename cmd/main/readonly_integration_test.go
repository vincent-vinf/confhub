package main

import (
	"bytes"
	"context"
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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestReadOnlyProcessHelper(t *testing.T) {
	if os.Getenv("CONFHUB_TEST_READONLY_PROCESS") != "1" {
		return
	}
	os.Args = []string{"confhub", "--read-only"}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// A real process with SELECT privileges must start, authenticate and subscribe
// without running DDL, bootstrap, audit, presence, or cleanup writes.
func TestReadOnlyProcessWithSelectOnlyDatabase(t *testing.T) {
	dsn := testutil.Database(t)
	ctx := context.Background()
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	writer, err := storage.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := writer.CreateUser(ctx, "reader", "reader-password", storage.UserInput{Enabled: true, Grants: []storage.Grant{{Namespace: "public", Group: "DEFAULT_GROUP", Access: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "readonly.yaml"}
	m, err := writer.Save(ctx, k, config.Edit{Content: "value: main\n", Format: "yaml", Description: "main description"})
	if err != nil {
		t.Fatal(err)
	}
	rules := []config.Rule{{ID: "beta", Name: "Beta", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"beta"}}}}}
	m, err = writer.SetRules(ctx, k, m.State.ID, m.State.Revision, rules, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err = writer.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Content: "value: beta\n", Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	readerDSN := testutil.ReadOnlyDSN(t, dsn)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	base := "http://" + address
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "readonly.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	process := exec.Command(binary, "-test.run=^TestReadOnlyProcessHelper$")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CONFHUB_") {
			process.Env = append(process.Env, value)
		}
	}
	process.Env = append(process.Env, "CONFHUB_TEST_READONLY_PROCESS=1", "CONFHUB_DSN="+readerDSN, "CONFHUB_LISTEN="+address,
		"CONFHUB_JWT_SECRET=read-only-test-secret-at-least-32-bytes", "CONFHUB_CLEANUP_INTERVAL=20ms")
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	t.Cleanup(func() {
		_ = process.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if err != nil {
				raw, _ := os.ReadFile(logPath)
				t.Errorf("read-only process: %v\n%s", err, raw)
			}
		case <-time.After(5 * time.Second):
			_ = process.Process.Kill()
			<-done
			t.Error("read-only process did not stop")
		}
		raw, _ := os.ReadFile(logPath)
		if strings.Contains(string(raw), "maintenance batch failed") || strings.Contains(string(raw), "presence refresh failed") || strings.Contains(string(raw), "audit start failed") {
			t.Errorf("read-only process attempted background writes:\n%s", raw)
		}
	})
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := client.Get(base + "/health/ready")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			raw, _ := os.ReadFile(logPath)
			t.Fatalf("read-only startup failed: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(raw))
		req.Header.Set("Origin", base)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, res.StatusCode, want, data)
		}
		return data
	}
	path := "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/readonly.yaml"
	call("GET", path, nil, 401)
	call("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "wrong-password"}, 401)
	call("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "initial-password"}, 200)
	var identity struct {
		ReadOnly    bool `json:"read_only"`
		SystemAdmin bool `json:"system_admin"`
	}
	if err := json.Unmarshal(call("GET", "/api/admin/me", nil, 200), &identity); err != nil || !identity.ReadOnly || !identity.SystemAdmin {
		t.Fatal("missing readonly identity", identity, err)
	}
	for _, route := range []string{path, path + "/versions", path + "/versions/1", "/api/admin/namespaces", "/api/admin/namespaces/public/groups", "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs", "/api/admin/audits", "/api/admin/users"} {
		call("GET", route, nil, 200)
	}
	var simulated config.Effective
	if err := json.Unmarshal(call("POST", path+"/simulate", map[string]any{"tags": map[string]string{"env": "beta"}}, 200), &simulated); err != nil || !simulated.Beta || simulated.Content != "value: beta\n" {
		t.Fatal(simulated, err)
	}
	for _, route := range []struct{ method, path string }{
		{"PUT", path}, {"DELETE", path}, {"PUT", path + "/rules"}, {"POST", path + "/rollback"}, {"POST", path + "/promote"},
		{"POST", "/api/admin/configs/batch-delete"}, {"POST", "/api/admin/configs/batch-move"},
		{"POST", "/api/admin/namespaces"}, {"DELETE", "/api/admin/namespaces/public"},
		{"POST", "/api/admin/namespaces/public/groups"}, {"DELETE", "/api/admin/namespaces/public/groups/DEFAULT_GROUP"},
		{"POST", "/api/admin/users"}, {"PUT", "/api/admin/users/unknown"}, {"DELETE", "/api/admin/users/unknown"},
		{"POST", "/api/admin/users/unknown/password"}, {"POST", "/api/admin/password"}, {"PATCH", "/api/admin/unknown"},
		{"POST", "/api/admin/unknown/simulate"}, {"PUT", "/api/admin/login"},
		{"GET", "/api/admin/clients"}, {"GET", "/api/admin/client-tags"},
	} {
		call(route.method, route.path, map[string]any{}, 403)
	}
	call("POST", "/api/admin/logout", nil, 204)
	call("GET", path, nil, 401)
	call("POST", "/api/admin/login", map[string]string{"username": "reader", "password": "reader-password"}, 200)
	call("GET", path, nil, 200)
	call("GET", "/api/admin/namespaces/public/groups/private/configs/secret", nil, 403)
	call("POST", path+"/simulate", map[string]any{"tags": map[string]string{"env": "beta"}}, 200)
	call("GET", "/api/admin/audits", nil, 403)
	if _, err := writer.UpdateUser(ctx, viewer.ID, viewer.Revision, storage.UserInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	call("GET", path, nil, 401)

	query := url.Values{"namespace": {"public"}, "group": {"DEFAULT_GROUP"}, "name": {"readonly.yaml"}}
	call("GET", "/api/client/config?"+query.Encode(), nil, 200)
	query.Set("tags", `{"env":"beta"}`)
	var effective config.Effective
	if err := json.Unmarshal(call("GET", "/api/client/config?"+query.Encode(), nil, 200), &effective); err != nil || !effective.Beta {
		t.Fatal(effective, err)
	}
	watch := func(tags string) *websocket.Conn {
		t.Helper()
		connection, _, err := websocket.DefaultDialer.Dial("ws://"+address+"/api/client/watch?tags="+url.QueryEscape(tags), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { connection.Close() })
		if err := connection.WriteJSON(map[string]any{"op": "subscribe", "key": k}); err != nil {
			t.Fatal(err)
		}
		return connection
	}
	mainClient, betaClient := watch(`{}`), watch(`{"env":"beta"}`)
	next := func(connection *websocket.Conn, content string, deleted bool) {
		t.Helper()
		connection.SetReadDeadline(time.Now().Add(3 * time.Second))
		var value config.Effective
		if err := connection.ReadJSON(&value); err != nil {
			t.Fatal(err)
		}
		if value.Content != content || value.Deleted != deleted {
			t.Fatalf("unexpected snapshot: %+v", value)
		}
	}
	next(mainClient, "value: main\n", false)
	next(betaClient, "value: beta\n", false)
	m, err = writer.Save(ctx, k, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "value: new\n", Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	next(mainClient, "value: new\n", false)
	m, err = writer.SetRules(ctx, k, m.State.ID, m.State.Revision, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	next(betaClient, "value: new\n", false)
	if _, err := writer.Delete(ctx, k, m.State.ID, m.State.Revision); err != nil {
		t.Fatal(err)
	}
	next(mainClient, "", true)
	next(betaClient, "", true)

	// Span the normal five-second presence interval; cleanup is configured at
	// 20ms above. The process must never log failed attempts against the replica.
	time.Sleep(5200 * time.Millisecond)
	audits, err := writer.Audits(ctx, storage.AuditFilter{Limit: 100})
	if err != nil || len(audits.Audits) != 0 {
		t.Fatal("read-only requests wrote audit data", audits, err)
	}
}
