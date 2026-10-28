package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/syncer"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestEdgeConsoleRequiresLoginAndRejectsManagementWrites(t *testing.T) {
	store := testutil.Store(t)
	if err := store.InitializeAdmin(context.Background(), "edge-test-password"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := syncer.New(storage.MirrorSource{Store: store}, syncer.Options{PollInterval: time.Millisecond})
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(ctx) }()
	defer func() { cancel(); <-done }()
	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("edge console"), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := server.New(store, hub, server.Options{Edge: true, JWTSecret: "edge-only-secret-at-least-32-bytes", JWTExpiry: time.Hour, StaticDir: static})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/", 200}, {"GET", "/configs", 200}, {"GET", "/api/admin/me", 401}, {"PUT", "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/a", 403}, {"GET", "/api/admin/namespaces", 401}, {"GET", "/api/replication/watch", 404}, {"GET", "/health/ready", 503}, {"GET", "/api/client/config?name=a", 503}, {"GET", "/api/edge/status", 200}, {"GET", "/api/admin/edge/status", 401}} {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.path, nil)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatal(tc, response.StatusCode)
		}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	call := func(method, path, body string, status int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		raw, _ := io.ReadAll(r.Body)
		if r.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, r.StatusCode, raw)
		}
		return raw
	}
	call("POST", "/api/admin/login", `{"username":"admin","password":"wrong"}`, 401)
	call("POST", "/api/admin/login", `{"username":"admin","password":"edge-test-password"}`, 200)
	var me struct {
		Username string `json:"username"`
		ReadOnly bool   `json:"read_only"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(call("GET", "/api/admin/me", "", 200), &me); err != nil {
		t.Fatal(err)
	}
	if me.Username != "admin" || !me.ReadOnly || me.Role != "edge" {
		t.Fatalf("unexpected edge identity: %+v", me)
	}
	call("GET", "/api/admin/edge/status", "", 200)
	call("GET", "/api/admin/namespaces", "", 503)
	call("GET", "/api/admin/clients", "", 200)
	cloud := testutil.Store(t)
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "demo.yaml"}
	m, err := cloud.Save(ctx, key, config.Edit{Content: "message: old", Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = cloud.Save(ctx, key, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "message: current", Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	m, err = cloud.SetRules(ctx, key, m.State.ID, m.State.Revision, []config.Rule{{ID: "gray", Name: "beta clients", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"beta"}}}}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	m, err = cloud.Save(ctx, key, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Target: "beta", Content: "message: beta", Format: "yaml"})
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	batch, err := cloud.Export(ctx, replication.Cursor{}, &data)
	if err != nil {
		t.Fatal(err)
	}
	lease, ok, err := store.AcquireMirrorLease(ctx, "test-owner", time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := store.ApplyMirror(ctx, lease, batch, &data); err != nil {
		t.Fatal(err)
	}
	if got := string(call("GET", "/api/admin/namespaces", "", 200)); got != `["public"]` {
		t.Fatal(got)
	}
	if got := string(call("GET", "/api/admin/namespaces/public/groups", "", 200)); got != `["DEFAULT_GROUP"]` {
		t.Fatal(got)
	}
	base := "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs"
	var list []storage.ConfigSummary
	if err := json.Unmarshal(call("GET", base, "", 200), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].GlobalVersion != 2 || list[0].Key.Name != "demo.yaml" {
		t.Fatal(list)
	}
	var mirrored config.State
	if err := json.Unmarshal(call("GET", base+"/demo.yaml", "", 200), &mirrored); err != nil {
		t.Fatal(err)
	}
	if mirrored.ID != m.State.ID || mirrored.Versions[2].Content != "message: current" || mirrored.Beta.Content != "message: beta" || len(mirrored.Rules) != 1 {
		t.Fatal(mirrored)
	}
	var history storage.VersionPage
	if err := json.Unmarshal(call("GET", base+"/demo.yaml/versions", "", 200), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Versions) != 1 || history.Versions[0].Number != 2 || history.Versions[0].Content != "" {
		t.Fatal(history)
	}
	call("GET", base+"/demo.yaml/versions/1", "", 404)
	call("GET", base+"/demo.yaml/versions/2", "", 200)
	var effective config.Effective
	if err := json.Unmarshal(call("POST", base+"/demo.yaml/simulate", `{"tags":{"env":"beta"}}`, 200), &effective); err != nil {
		t.Fatal(err)
	}
	if !effective.Beta || effective.Content != "message: beta" {
		t.Fatal(effective)
	}
	for _, path := range []string{"/api/admin/namespaces", "/api/admin/password", "/api/admin/users", "/api/admin/configs/batch-move", "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/a/rollback"} {
		call("POST", path, `{}`, 403)
	}
	for _, path := range []string{"/api/admin/users", "/api/admin/audits"} {
		call("GET", path, "", 404)
	}
	call("POST", "/api/admin/logout", `{}`, 204)
	call("GET", "/api/admin/me", "", 401)
}

func TestReplicationAuthenticationDoesNotAcceptAdminCookiesOrBrowserOrigin(t *testing.T) {
	store := testutil.Store(t)
	app, err := server.New(store, nil, server.Options{JWTSecret: "01234567890123456789012345678901", JWTExpiry: time.Hour, ReplicationToken: "replication-012345678901234567890123456"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	for _, tc := range []struct {
		authorization, origin string
		status                int
	}{{"", "", 401}, {"Bearer wrong", "", 401}, {"Bearer replication-012345678901234567890123456", ts.URL, 403}} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/replication/watch", nil)
		req.Header.Set("Authorization", tc.authorization)
		req.Header.Set("Origin", tc.origin)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.status {
			t.Fatal(response.StatusCode, tc.status)
		}
	}
}
