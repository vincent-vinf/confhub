package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/syncer"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestBatchEndpointsProtectMutationsAndNotifyOtherReplica(t *testing.T) {
	store := testutil.Store(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldKey := config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "batch"}
	newKey := config.Key{Namespace: "public", Group: "target", Name: "batch"}
	if err := store.CreateGroup(ctx, "public", "target"); err != nil {
		t.Fatal(err)
	}
	m, err := store.Save(ctx, oldKey, config.Edit{Content: "keep", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	secret := "0123456789abcdef0123456789abcdef"
	hub := syncer.New(store, syncer.Options{PollInterval: 10 * time.Millisecond})
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for !hub.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("observer not ready")
		}
		time.Sleep(time.Millisecond)
	}
	newServer := func(hub *syncer.Hub) *httptest.Server {
		app, err := server.New(store, hub, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(app.Handler())
	}
	observer, writer := newServer(hub), newServer(nil)
	defer observer.Close()
	defer writer.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(observer.URL, "http")+"/api/client/watch", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	next := func() config.Effective {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var value config.Effective
		if err := conn.ReadJSON(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, key := range []config.Key{oldKey, newKey} {
		if err := conn.WriteJSON(map[string]any{"op": "subscribe", "key": key}); err != nil {
			t.Fatal(err)
		}
		initial := next()
		if initial.Key != key || initial.Deleted != (key == newKey) {
			t.Fatal("initial watch", initial)
		}
	}
	token := adminToken(t, store, secret)
	post := func(path string, body any, authenticated bool, origin string, want int) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", writer.URL+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s: status %d, want %d", path, res.StatusCode, want)
		}
	}
	targets := []storage.ConfigTarget{{Key: oldKey, ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision}}
	body := map[string]any{"configs": targets, "namespace": "public", "group": "target", "confirmed": true}
	movePath, deletePath := "/api/admin/configs/batch-move", "/api/admin/configs/batch-delete"
	post(movePath, body, false, writer.URL, 401)
	post(movePath, body, true, "https://other.example", 403)
	body["confirmed"] = false
	post(movePath, body, true, writer.URL, 400)
	post(deletePath, map[string]any{"configs": targets}, true, writer.URL, 400)
	body["confirmed"] = true
	body["unexpected"] = true
	post(movePath, body, true, writer.URL, 400)
	delete(body, "unexpected")
	post(movePath, body, true, writer.URL, 200)
	updates := map[config.Key]config.Effective{}
	for i := 0; i < 2; i++ {
		v := next()
		updates[v.Key] = v
	}
	if !updates[oldKey].Deleted || updates[newKey].Deleted || updates[newKey].ID != m.State.ID || updates[newKey].Content != "keep" {
		t.Fatal("other replica did not receive both move notifications", updates)
	}
	post(movePath, body, true, writer.URL, 404) // An old editor cannot move the departed source.
	deleteBody := map[string]any{"configs": []storage.ConfigTarget{{Key: newKey, ExpectedID: m.State.ID, ExpectedRevision: m.State.Revision + 1}}, "confirmed": true}
	post(deletePath, deleteBody, false, writer.URL, 401)
	post(deletePath, deleteBody, true, writer.URL, 200)
	if deleted := next(); !deleted.Deleted || deleted.Key != newKey {
		t.Fatal("batch deletion not pushed", deleted)
	}
}
