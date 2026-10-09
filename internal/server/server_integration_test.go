package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/syncer"

	"gitlab.bodesitech.com/bodesi/confhub/internal/server"
	"gitlab.bodesitech.com/bodesi/confhub/internal/testutil"
)

func TestAdminAuthenticationAndSameOriginProtection(t *testing.T) {
	store := testutil.Store(t)
	if err := store.InitializeAdmin(context.Background(), "initial-password"); err != nil {
		t.Fatal(err)
	}
	// Restarts cannot replace an existing password.
	if err := store.InitializeAdmin(context.Background(), "replacement-password"); err != nil {
		t.Fatal(err)
	}
	app, err := server.New(store, nil, server.Options{JWTSecret: "0123456789abcdef0123456789abcdef", JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	request := func(path string, body any, cookie *http.Cookie, origin string) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	bad := request("/api/admin/login", map[string]string{"username": "admin", "password": "replacement-password"}, nil, ts.URL)
	if bad.StatusCode != 401 {
		t.Fatalf("restart changed password: %d", bad.StatusCode)
	}
	res := request("/api/admin/login", map[string]string{"username": "admin", "password": "initial-password"}, nil, ts.URL)
	if res.StatusCode != 200 || len(res.Cookies()) != 1 {
		t.Fatalf("login: %d", res.StatusCode)
	}
	cookie := res.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie flags")
	}
	blocked := request("/api/admin/password", map[string]string{"old_password": "initial-password", "new_password": "updated-password"}, cookie, "https://evil.example")
	if blocked.StatusCode != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	changed := request("/api/admin/password", map[string]string{"old_password": "initial-password", "new_password": "updated-password"}, cookie, ts.URL)
	if changed.StatusCode != 200 {
		t.Fatalf("password change: %d", changed.StatusCode)
	}
	if updated := request("/api/admin/login", map[string]string{"username": "admin", "password": "updated-password"}, nil, ts.URL); updated.StatusCode != 200 {
		t.Fatal("updated password cannot log in")
	}
	if old := request("/api/admin/login", map[string]string{"username": "admin", "password": "initial-password"}, nil, ts.URL); old.StatusCode != 401 {
		t.Fatal("old password still accepted")
	}
	r, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/namespaces", nil)
	r.AddCookie(cookie)
	stillValid, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer stillValid.Body.Close()
	if stillValid.StatusCode != 200 {
		t.Fatal("password change unexpectedly revoked pure JWT")
	}
}

func TestManagementRequiresConfirmationAndClientReadsAreAnonymous(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	if err := store.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	app, err := server.New(store, nil, server.Options{JWTSecret: "0123456789abcdef0123456789abcdef", JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	request := func(method, path string, body any, cookie *http.Cookie) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", ts.URL)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	login := request("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "initial-password"}, nil)
	cookie := login.Cookies()[0]
	path := "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/service"
	if res := request("PUT", path, map[string]any{"content": "one", "format": "text"}, cookie); res.StatusCode != 400 {
		t.Fatalf("unconfirmed save: %d", res.StatusCode)
	}
	res := request("PUT", path, map[string]any{"content": "one", "format": "text", "confirmed": true}, cookie)
	if res.StatusCode != 200 {
		t.Fatalf("save: %d", res.StatusCode)
	}
	var mutation struct {
		State struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&mutation); err != nil {
		t.Fatal(err)
	}
	res = request("GET", "/api/client/config?name=service", nil, nil)
	if res.StatusCode != 200 {
		t.Fatalf("anonymous get: %d", res.StatusCode)
	}
	var effective struct {
		Content string `json:"content"`
		Version int64  `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&effective); err != nil {
		t.Fatal(err)
	}
	if effective.Content != "one" || effective.Version != 1 {
		t.Fatal("incorrect client state")
	}
	res = request("PUT", path, map[string]any{"content": "two", "format": "text", "confirmed": true, "expected_id": mutation.State.ID, "expected_revision": 0}, cookie)
	if res.StatusCode != 409 {
		t.Fatalf("stale editor accepted: %d", res.StatusCode)
	}
	if res = request("GET", path+"/versions", nil, cookie); res.StatusCode != 200 {
		t.Fatalf("history: %d", res.StatusCode)
	}
}

func TestWebSocketPushesPublicationDeletionAndRecreation(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := syncer.New(store, syncer.Options{PollInterval: 10 * time.Millisecond, FailureTimeout: time.Second, CacheBytes: 1024})
	go hub.Run(ctx)
	deadline := time.Now().Add(time.Second)
	for !hub.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	app, err := server.New(store, hub, server.Options{JWTSecret: "0123456789abcdef0123456789abcdef", JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	u.Scheme = "ws"
	u.Path = "/api/client/watch"
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service"}
	if err = conn.WriteJSON(map[string]any{"op": "subscribe", "key": k}); err != nil {
		t.Fatal(err)
	}
	next := func() config.Effective {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		var v config.Effective
		if err := conn.ReadJSON(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := next(); !v.Deleted {
		t.Fatal("missing config must produce explicit absence")
	}
	m, err := store.Save(ctx, k, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	v := next()
	if v.Content != "one" || v.Version != 1 || v.Deleted {
		t.Fatalf("initial publication: %+v", v)
	}
	if _, err = store.Delete(ctx, k, m.State.ID, m.State.Revision); err != nil {
		t.Fatal(err)
	}
	deleted := next()
	if !deleted.Deleted || deleted.ID != m.State.ID || deleted.Sequence <= v.Sequence {
		t.Fatalf("deletion: %+v", deleted)
	}
	recreated, err := store.Save(ctx, k, config.Edit{Content: "new", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	v = next()
	if v.ID == m.State.ID || v.ID != recreated.State.ID || v.Content != "new" || v.Version != 1 {
		t.Fatalf("recreation: %+v", v)
	}
}

func TestStaticPagesFallbackWithoutMaskingUnknownAPIsOrAssets(t *testing.T) {
	store := testutil.Store(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ConfHub</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := server.New(store, nil, server.Options{JWTSecret: "0123456789abcdef0123456789abcdef", JWTExpiry: time.Hour, StaticDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	for _, tc := range []struct {
		path   string
		status int
	}{{"/configs/service", 200}, {"/configs/public/DEFAULT_GROUP/application.json", 200}, {"/configs/public/DEFAULT_GROUP/application.yaml", 200}, {"/configs/public/DEFAULT_GROUP/missing.json/asset.js", 404}, {"/api/missing", 404}, {"/missing.js", 404}, {"/assets/missing", 404}, {"/api/client/missing", 404}} {
		res, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s: got %d expected %d", tc.path, res.StatusCode, tc.status)
		}
	}
}

func TestAdminRejectsExpiredUnsignedAndWrongAlgorithmTokens(t *testing.T) {
	store := testutil.Store(t)
	secret := "0123456789abcdef0123456789abcdef"
	app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	for _, tc := range []struct {
		name   string
		method jwt.SigningMethod
		claims jwt.RegisteredClaims
		secret string
	}{
		{"expired", jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "admin", Issuer: "confhub", ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}, secret},
		{"missing expiry", jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "admin", Issuer: "confhub"}, secret},
		{"wrong algorithm", jwt.SigningMethodHS512, jwt.RegisteredClaims{Subject: "admin", Issuer: "confhub", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, secret},
		{"wrong secret", jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "admin", Issuer: "confhub", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, "different-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := jwt.NewWithClaims(tc.method, tc.claims).SignedString([]byte(tc.secret))
			if err != nil {
				t.Fatal(err)
			}
			r, _ := http.NewRequest("GET", ts.URL+"/api/admin/namespaces", nil)
			r.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
			res, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != 401 {
				t.Fatalf("invalid JWT accepted: %d", res.StatusCode)
			}
		})
	}
}

func TestHttpPublicationOnOneReplicaPushesThroughAnother(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	newReplica := func() *httptest.Server {
		t.Helper()
		hub := syncer.New(store, syncer.Options{PollInterval: 5 * time.Millisecond, FailureTimeout: time.Second, CacheBytes: 4096})
		go hub.Run(ctx)
		deadline := time.Now().Add(time.Second)
		for !hub.Ready() {
			if time.Now().After(deadline) {
				t.Fatal("not ready")
			}
			time.Sleep(time.Millisecond)
		}
		app, err := server.New(store, hub, server.Options{JWTSecret: "0123456789abcdef0123456789abcdef", JWTExpiry: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(app.Handler())
	}
	admin, client := newReplica(), newReplica()
	defer admin.Close()
	defer client.Close()
	u, _ := url.Parse(client.URL)
	u.Scheme = "ws"
	u.Path = "/api/client/watch"
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	k := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "service"}
	if err = conn.WriteJSON(map[string]any{"op": "subscribe", "key": k}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var missing config.Effective
	if err = conn.ReadJSON(&missing); err != nil {
		t.Fatal(err)
	}
	post := func(method, path string, body any, cookie *http.Cookie) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		r, _ := http.NewRequest(method, admin.URL+path, bytes.NewReader(raw))
		r.Header.Set("Origin", admin.URL)
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	login := post("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "initial-password"}, nil)
	cookie := login.Cookies()[0]
	saved := post("PUT", "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/service", map[string]any{"confirmed": true, "content": "cross-node", "format": "text"}, cookie)
	if saved.StatusCode != 200 {
		t.Fatalf("save: %d", saved.StatusCode)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var value config.Effective
	if err = conn.ReadJSON(&value); err != nil {
		t.Fatal(err)
	}
	if value.Content != "cross-node" || value.Version != 1 {
		t.Fatalf("cross-node push: %+v", value)
	}
}
