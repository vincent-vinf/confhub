package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"gitlab.bodesitech.com/bodesi/confhub/internal/clientinfo"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/server"
	"gitlab.bodesitech.com/bodesi/confhub/internal/syncer"
	"gitlab.bodesitech.com/bodesi/confhub/internal/testutil"
)

func TestClientsAreReadOnlyAuthenticatedAndVisibleAcrossReplicas(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "presence"}
	m, err := store.Save(ctx, key, config.Edit{Content: "secret-config-content", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	newReplica := func() *httptest.Server {
		h := syncer.New(store, syncer.Options{PollInterval: 10 * time.Millisecond})
		go h.Run(ctx)
		deadline := time.Now().Add(time.Second)
		for !h.Ready() {
			if time.Now().After(deadline) {
				t.Fatal("not ready")
			}
			time.Sleep(time.Millisecond)
		}
		app, err := server.New(store, h, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		go app.RunPresence(ctx)
		return httptest.NewServer(app.Handler())
	}
	a, b := newReplica(), newReplica()
	defer a.Close()
	defer b.Close()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Subject: "admin", Issuer: "confhub", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, auth bool) *http.Response {
		t.Helper()
		r, _ := http.NewRequest("GET", a.URL+path, nil)
		if auth {
			r.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := get("/api/admin/clients", false)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("anonymous metadata exposed", res.StatusCode)
	}
	// A plain GET or a failed upgrade never appears as an online connection.
	res, err = http.Get(b.URL + "/api/client/config?name=presence")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res, err = http.Get(b.URL + "/api/client/watch")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	u, _ := url.Parse(b.URL)
	u.Scheme = "ws"
	u.Path = "/api/client/watch"
	u.RawQuery = "tags=" + url.QueryEscape(`{"env":"gray","sys.ip":"192.168.2.3","sys.hostname":"node-test"}`)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteJSON(map[string]any{"op": "subscribe", "key": key}); err != nil {
		t.Fatal(err)
	}
	next := func() {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		var value config.Effective
		if err = conn.ReadJSON(&value); err != nil {
			t.Fatal(err)
		}
	}
	next()
	wait := func(check func(clientinfo.Page) bool) clientinfo.Page {
		t.Helper()
		deadline := time.Now().Add(7 * time.Second)
		for {
			res := get("/api/admin/clients", true)
			var p clientinfo.Page
			err := json.NewDecoder(res.Body).Decode(&p)
			res.Body.Close()
			if err != nil || res.StatusCode != 200 {
				t.Fatalf("list: %d %v", res.StatusCode, err)
			}
			if check(p) {
				return p
			}
			if time.Now().After(deadline) {
				t.Fatalf("presence never converged: %+v", p)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	p := wait(func(p clientinfo.Page) bool {
		return len(p.Clients) == 1 && len(p.Clients[0].Subscriptions) == 1 && p.Clients[0].Subscriptions[0].Sent
	})
	c := p.Clients[0]
	if c.Tags["env"] != "gray" || c.InstanceID == "" || c.SourceAddress == "" || c.ConnectedAt.IsZero() || c.RefreshedAt.IsZero() || c.Subscriptions[0].Version != 1 {
		t.Fatalf("metadata: %+v", c)
	}
	res = get("/api/admin/client-tags?tag=env&prefix=gr", true)
	var values []string
	err = json.NewDecoder(res.Body).Decode(&values)
	res.Body.Close()
	if err != nil || len(values) != 1 || values[0] != "gray" {
		t.Fatal(values, err)
	}
	m, err = store.Save(ctx, key, config.Edit{ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision, Content: "new-secret", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	next()
	wait(func(p clientinfo.Page) bool {
		return len(p.Clients) == 1 && len(p.Clients[0].Subscriptions) == 1 && p.Clients[0].Subscriptions[0].Version == 2
	})
	if err = conn.WriteJSON(map[string]any{"op": "unsubscribe", "key": key}); err != nil {
		t.Fatal(err)
	}
	wait(func(p clientinfo.Page) bool { return len(p.Clients) == 1 && len(p.Clients[0].Subscriptions) == 0 })
	conn.Close()
	wait(func(p clientinfo.Page) bool { return len(p.Clients) == 0 })
}
