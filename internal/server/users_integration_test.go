package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func adminToken(t *testing.T, store *storage.Store, secret string) string {
	t.Helper()
	ctx := context.Background()
	if err := store.InitializeAdmin(ctx, "initial-password"); err != nil {
		t.Fatal(err)
	}
	u, err := store.Authenticate(ctx, "admin", "initial-password")
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": u.ID, "iss": "confhub", "exp": time.Now().Add(time.Hour).Unix(), "auth_version": u.AuthVersion}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func TestUserPermissionsAcrossReplicasAndRevocation(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	secret := "0123456789abcdef0123456789abcdef"
	token := adminToken(t, store, secret)
	replica := func() *httptest.Server {
		app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(app.Handler())
	}
	a, b := replica(), replica()
	defer a.Close()
	defer b.Close()
	request := func(ts *httptest.Server, method, path string, body any, cookie string, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", ts.URL)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: cookie})
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: want %d got %d: %s", method, path, want, res.StatusCode, out)
		}
		return out
	}
	login := func(name, password string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"username": name, "password": password})
		req, _ := http.NewRequest("POST", a.URL+"/api/admin/login", bytes.NewReader(raw))
		req.Header.Set("Origin", a.URL)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("login %s: %d", name, res.StatusCode)
		}
		return res.Cookies()[0].Value
	}
	for _, ns := range []string{"dev", "prod"} {
		if err := store.CreateNamespace(ctx, ns); err != nil {
			t.Fatal(err)
		}
		for _, g := range []string{"apps", "secret"} {
			if err := store.CreateGroup(ctx, ns, g); err != nil {
				t.Fatal(err)
			}
		}
	}
	key := config.Key{Namespace: "dev", Group: "apps", Name: "app"}
	m, err := store.Save(ctx, key, config.Edit{Content: "one", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(ctx, config.Key{Namespace: "prod", Group: "apps", Name: "app"}, config.Edit{Content: "production", Format: "text"}); err != nil {
		t.Fatal(err)
	}
	input := storage.UserInput{Enabled: true, Grants: []storage.Grant{{Namespace: "dev", Access: "write"}, {Namespace: "prod", Group: "apps", Access: "read"}}}
	created := request(a, "POST", "/api/admin/users", map[string]any{"username": "operator", "password": "operator-password", "enabled": true, "grants": input.Grants}, token, 200)
	var user storage.User
	if err = json.Unmarshal(created, &user); err != nil {
		t.Fatal(err)
	}
	userToken := login("operator", "operator-password")
	me := request(b, "GET", "/api/admin/me", nil, userToken, 200)
	if bytes.Contains(me, []byte("password")) || bytes.Contains(me, []byte("auth_version")) {
		t.Fatal("secrets leaked", string(me))
	}
	request(b, "GET", "/api/admin/namespaces", nil, userToken, 200)
	var namespaces []string
	json.Unmarshal(request(b, "GET", "/api/admin/namespaces", nil, userToken, 200), &namespaces)
	if len(namespaces) != 2 || namespaces[0] != "dev" || namespaces[1] != "prod" {
		t.Fatal(namespaces)
	}
	if got := string(request(b, "GET", "/api/admin/namespaces/prod/groups", nil, userToken, 200)); got != "[\"apps\"]" {
		t.Fatal(got)
	}
	base := "/api/admin/namespaces/prod/groups/apps/configs/app"
	for _, suffix := range []string{"", "/versions", "/versions/1"} {
		request(b, "GET", base+suffix, nil, userToken, 200)
	}
	request(b, "POST", base+"/simulate", map[string]any{"tags": map[string]string{}}, userToken, 200)
	for _, tc := range []struct{ method, path string }{
		{"PUT", base}, {"DELETE", base}, {"PUT", base + "/rules"}, {"POST", base + "/rollback"}, {"POST", base + "/promote"},
		{"GET", "/api/admin/namespaces/prod/groups/secret/configs"}, {"GET", "/api/admin/namespaces/public/groups"},
		{"GET", "/api/admin/clients"}, {"GET", "/api/admin/client-tags"}, {"GET", "/api/admin/users"}, {"POST", "/api/admin/users"}, {"PUT", "/api/admin/users/" + user.ID}, {"DELETE", "/api/admin/users/" + user.ID}, {"POST", "/api/admin/users/" + user.ID + "/password"},
		{"POST", "/api/admin/namespaces"}, {"DELETE", "/api/admin/namespaces/dev"}, {"POST", "/api/admin/namespaces/dev/groups"}, {"DELETE", "/api/admin/namespaces/dev/groups/apps"},
	} {
		request(b, tc.method, tc.path, map[string]any{"confirmed": true}, userToken, 403)
	}
	target := storage.ConfigTarget{Key: key, ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision}
	body := map[string]any{"configs": []storage.ConfigTarget{target}, "namespace": "prod", "group": "apps", "confirmed": true}
	request(b, "POST", "/api/admin/configs/batch-move", body, userToken, 403)
	prod, err := store.Snapshot(ctx, config.Key{Namespace: "prod", Group: "apps", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	body = map[string]any{"configs": []storage.ConfigTarget{target, {Key: prod.Key, ExpectedID: prod.ID, ExpectedRevision: prod.Revision}}, "confirmed": true}
	request(b, "POST", "/api/admin/configs/batch-delete", body, userToken, 403)
	if _, err = store.Snapshot(ctx, key); err != nil {
		t.Fatal("partially deleted", err)
	}
	devBase := "/api/admin/namespaces/dev/groups/apps/configs/app"
	request(b, "PUT", devBase, map[string]any{"content": "two", "format": "text", "expected_id": m.State.ID, "expected_revision": m.State.Revision, "confirmed": true}, userToken, 200)
	// User list pagination exposes no hashes, and non-admin users cannot grant themselves privileges.
	users := request(a, "GET", "/api/admin/users?limit=1", nil, token, 200)
	if bytes.Contains(users, []byte("password")) {
		t.Fatal("hash leaked")
	}
	request(a, "GET", "/api/admin/users?limit=101", nil, token, 400)
	// Downgrade and grant changes immediately apply on the other replica with the same JWT.
	input.Grants = []storage.Grant{{Namespace: "prod", Group: "apps", Access: "read"}}
	input.ClientsRead = true
	updated := request(a, "PUT", "/api/admin/users/"+user.ID, map[string]any{"enabled": true, "clients_read": true, "grants": input.Grants, "expected_revision": user.Revision, "confirmed": true}, token, 200)
	json.Unmarshal(updated, &user)
	request(b, "GET", devBase, nil, userToken, 403)
	request(b, "GET", "/api/admin/clients", nil, userToken, 200)
	request(b, "GET", "/api/admin/client-tags", nil, userToken, 200)
	// Reset invalidates old sessions across replicas.
	request(a, "POST", "/api/admin/users/"+user.ID+"/password", map[string]any{"password": "replacement-password", "expected_revision": user.Revision, "confirmed": true}, token, 200)
	request(b, "GET", "/api/admin/me", nil, userToken, 401)
	userToken = login("operator", "replacement-password")
	user, err = store.User(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	request(a, "PUT", "/api/admin/users/"+user.ID, map[string]any{"enabled": false, "expected_revision": user.Revision, "confirmed": true}, token, 200)
	request(b, "GET", "/api/admin/me", nil, userToken, 401)
	user, err = store.User(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateUser(ctx, user.ID, user.Revision, storage.UserInput{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	request(b, "GET", "/api/admin/me", nil, userToken, 401) // Enabling cannot resurrect the previous JWT.
	fresh := login("operator", "replacement-password")
	request(a, "DELETE", "/api/admin/users/"+user.ID, map[string]any{"expected_revision": user.Revision, "confirmed": true}, token, 200)
	request(b, "GET", "/api/admin/me", nil, fresh, 401)
	request(b, "GET", "/api/client/config?namespace=prod&group=apps&name=app", nil, "", 200)
}
