package server_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestManagementAuditOperationsPermissionsAndSecrets(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	secret := "0123456789abcdef0123456789abcdef"
	token := adminToken(t, store, secret)
	app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, cookie string, want int) (string, []byte) {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://confhub.test"+path, bytes.NewReader(raw))
		r.RemoteAddr = "192.168.2.5:12345"
		r.Header.Set("Origin", "http://confhub.test")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "10.0.0.9")
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: "confhub_admin", Value: cookie})
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: want %d got %d: %s", method, path, want, w.Code, w.Body.String())
		}
		return w.Header().Get("X-Request-ID"), w.Body.Bytes()
	}
	check := func(id, action, actor, result string, status int) storage.Audit {
		t.Helper()
		page, err := store.Audits(ctx, storage.AuditFilter{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Audits {
			if a.RequestID != id {
				continue
			}
			if a.Action != action || a.Username != actor || a.Result != result || a.Status != status || a.IP != "192.168.2.5" || a.CreatedAt.IsZero() {
				t.Fatalf("unexpected audit: %+v", a)
			}
			return a
		}
		t.Fatalf("missing audit for request %s", id)
		return storage.Audit{}
	}
	id, _ := request("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "initial-password"}, "", 200)
	a := check(id, "auth.login", "admin", "success", 200)
	if a.UserID == "" {
		t.Fatal("verified actor missing")
	}
	id, _ = request("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "secret-wrong-password"}, "", 401)
	a = check(id, "auth.login", "", "failure", 401)
	if a.AttemptedUsername != "admin" || a.UserID != "" {
		t.Fatal("failed attempt masquerades as verified user", a)
	}
	base := "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/audit.json"
	id, _ = request("GET", base, nil, "", 401)
	check(id, "config.read", "", "failure", 401)
	var state config.State
	save := func(body map[string]any, action string) {
		t.Helper()
		body["confirmed"] = true
		if state.ID != "" {
			body["expected_id"], body["expected_revision"] = state.ID, state.Revision
		}
		id, raw := request("PUT", base, body, token, 200)
		check(id, action, "admin", "success", 200)
		var mutation config.Mutation
		if err := json.Unmarshal(raw, &mutation); err != nil {
			t.Fatal(err)
		}
		state = *mutation.State
	}
	save(map[string]any{"content": `{"secret":"configuration-secret-one"}`, "format": "json"}, "config.publish")
	save(map[string]any{"content": `{"secret":"configuration-secret-two"}`, "format": "json"}, "config.publish")
	id, _ = request("GET", base, nil, token, 200)
	a = check(id, "config.read", "admin", "success", 200)
	if a.Subject != "public / DEFAULT_GROUP / audit.json" || !bytes.Contains(a.Details, []byte(`"global_version":2`)) {
		t.Fatal(a)
	}
	id, _ = request("GET", base+"/versions/1", nil, token, 200)
	check(id, "config.version.read", "admin", "success", 200)
	id, _ = request("POST", base+"/simulate", map[string]any{"tags": map[string]string{"env": "tag-secret"}}, token, 200)
	check(id, "config.simulate", "admin", "success", 200)
	id, raw := request("PUT", base+"/rules", map[string]any{"expected_id": state.ID, "expected_revision": state.Revision, "confirmed": true, "source_version": 1, "rules": []config.Rule{{ID: "audit-rule", Name: "灰度规则", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"tag-secret"}}}}}}, token, 200)
	a = check(id, "config.rules", "admin", "success", 200)
	if !bytes.Contains(a.Details, []byte(`"rule_count":1`)) {
		t.Fatal(a)
	}
	var mutation config.Mutation
	if err = json.Unmarshal(raw, &mutation); err != nil {
		t.Fatal(err)
	}
	state = *mutation.State
	save(map[string]any{"content": `{"secret":"configuration-beta-secret"}`, "format": "json", "target": "beta"}, "config.beta.save")
	for _, action := range []string{"promote", "rollback"} {
		source := 1
		if action == "promote" {
			source = 0
		}
		id, raw = request("POST", base+"/"+action, map[string]any{"expected_id": state.ID, "expected_revision": state.Revision, "confirmed": true, "source_version": source}, token, 200)
		check(id, "config."+action, "admin", "success", 200)
		if err = json.Unmarshal(raw, &mutation); err != nil {
			t.Fatal(err)
		}
		state = *mutation.State
	}
	// Concurrent writes from one snapshot produce one success and one conflict,
	// both with independent, complete audit records.
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, content := range []string{`{"value":10}`, `{"value":20}`} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]any{"expected_id": state.ID, "expected_revision": state.Revision, "confirmed": true, "content": content, "format": "json"})
			r := httptest.NewRequest("PUT", "http://confhub.test"+base, bytes.NewReader(raw))
			r.RemoteAddr = "192.168.2.5:12345"
			r.Header.Set("Origin", "http://confhub.test")
			r.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
			w := httptest.NewRecorder()
			app.Handler().ServeHTTP(w, r)
			results <- w
		}()
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for w := range results {
		result := "success"
		if w.Code == 200 {
			wins++
		} else if w.Code == 409 {
			conflicts++
			result = "failure"
		} else {
			t.Fatal(w.Code, w.Body.String())
		}
		check(w.Header().Get("X-Request-ID"), "config.publish", "admin", result, w.Code)
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
	id, raw = request("POST", "/api/admin/users", map[string]any{"username": "reader", "password": "reader-secret-password", "enabled": true, "grants": []storage.Grant{{Namespace: "public", Access: "read"}}}, token, 200)
	check(id, "user.create", "admin", "success", 200)
	var reader storage.User
	if err = json.Unmarshal(raw, &reader); err != nil {
		t.Fatal(err)
	}
	id, raw = request("POST", "/api/admin/login", map[string]string{"username": "reader", "password": "reader-secret-password"}, "", 200)
	check(id, "auth.login", "reader", "success", 200)
	// Get a reader cookie through a real login response.
	r := httptest.NewRequest("POST", "http://confhub.test/api/admin/login", strings.NewReader(`{"username":"reader","password":"reader-secret-password"}`))
	r.Header.Set("Origin", "http://confhub.test")
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	readerToken := w.Result().Cookies()[0].Value
	id, _ = request("PUT", base, map[string]any{"confirmed": true}, readerToken, 403)
	check(id, "config.publish", "reader", "failure", 403)
	request("GET", "/api/admin/audits", nil, readerToken, 403)
	r = httptest.NewRequest("PUT", "http://confhub.test"+base, strings.NewReader(`{}`))
	r.RemoteAddr = "192.168.2.5:12345"
	r.Header.Set("Origin", "https://untrusted.test")
	r.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	check(w.Header().Get("X-Request-ID"), "config.publish", "admin", "failure", 403)
	id, raw = request("PUT", "/api/admin/users/"+reader.ID, map[string]any{"enabled": true, "grants": []storage.Grant{{Namespace: "public", Access: "write"}}, "confirmed": true, "expected_revision": reader.Revision}, token, 200)
	check(id, "user.update", "admin", "success", 200)
	json.Unmarshal(raw, &reader)
	id, _ = request("POST", "/api/admin/users/"+reader.ID+"/password", map[string]any{"password": "replacement-secret-password", "confirmed": true, "expected_revision": reader.Revision}, token, 200)
	check(id, "user.password", "admin", "success", 200)
	reader, err = store.User(ctx, reader.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ = request("DELETE", "/api/admin/users/"+reader.ID, map[string]any{"confirmed": true, "expected_revision": reader.Revision}, token, 200)
	a = check(id, "user.delete", "admin", "success", 200)
	if a.Subject != "用户 / reader" {
		t.Fatal(a)
	}
	id, _ = request("POST", "/api/admin/namespaces", map[string]string{"name": "audit-target"}, token, 200)
	check(id, "namespace.create", "admin", "success", 200)
	id, _ = request("POST", "/api/admin/namespaces/audit-target/groups", map[string]string{"name": "apps"}, token, 200)
	check(id, "group.create", "admin", "success", 200)
	current, err := store.Snapshot(ctx, state.Key)
	if err != nil {
		t.Fatal(err)
	}
	id, _ = request("POST", "/api/admin/configs/batch-move", map[string]any{"configs": []storage.ConfigTarget{{Key: current.Key, ExpectedID: current.ID, ExpectedRevision: current.Revision}}, "namespace": "audit-target", "group": "apps", "confirmed": true}, token, 200)
	a = check(id, "config.batch-move", "admin", "success", 200)
	if !bytes.Contains(a.Details, []byte(`"target_namespace":"audit-target"`)) {
		t.Fatal(a)
	}
	current, err = store.Snapshot(ctx, config.Key{Namespace: "audit-target", Group: "apps", Name: "audit.json"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ = request("POST", "/api/admin/configs/batch-delete", map[string]any{"configs": []storage.ConfigTarget{{Key: current.Key, ExpectedID: current.ID, ExpectedRevision: current.Revision}}, "confirmed": true}, token, 200)
	check(id, "config.batch-delete", "admin", "success", 200)
	id, _ = request("DELETE", "/api/admin/namespaces/audit-target/groups/apps", map[string]any{"confirmed": true}, token, 200)
	check(id, "group.delete", "admin", "success", 200)
	id, _ = request("DELETE", "/api/admin/namespaces/audit-target", map[string]any{"confirmed": true}, token, 200)
	check(id, "namespace.delete", "admin", "success", 200)
	deleted, err := store.Save(ctx, state.Key, config.Edit{Content: "delete-secret", Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ = request("DELETE", base, map[string]any{"expected_id": deleted.State.ID, "expected_revision": deleted.State.Revision, "confirmed": true}, token, 200)
	check(id, "config.delete", "admin", "success", 200)
	id, _ = request("POST", "/api/admin/logout", nil, token, 204)
	check(id, "auth.logout", "admin", "success", 204)
	page, err := store.Audits(ctx, storage.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := len(page.Audits)
	for _, path := range []string{"/api/admin/me", "/api/admin/namespaces", "/api/admin/namespaces/public/groups", "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs", "/api/admin/users", "/api/admin/audits", "/health/live"} {
		request("GET", path, nil, token, 200)
	}
	request("GET", "/api/client/config?name=audit.json", nil, "", 404)
	page, err = store.Audits(ctx, storage.AuditFilter{Limit: 100})
	if err != nil || len(page.Audits) != count {
		t.Fatal("background queries or SDK calls were audited", page, err)
	}
	encoded, _ := json.Marshal(page)
	for _, forbidden := range []string{"configuration-secret", "configuration-beta-secret", "tag-secret", "initial-password", "reader-secret-password", "replacement-secret-password", "secret-wrong-password", token, readerToken, "password_hash", "auth_version"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatal("audit leaked secret", forbidden)
		}
	}
	_, raw = request("GET", "/api/admin/audits?username=reader&result=failure&action=config.publish&ip=192.168.2.5&limit=1", nil, token, 200)
	json.Unmarshal(raw, &page)
	if len(page.Audits) != 1 || page.Audits[0].Username != "reader" {
		t.Fatal(string(raw))
	}
	for _, query := range []string{"before=bad", "limit=101", "ip=invalid", "result=other", "from=yesterday", "from=2026-10-12T12:00:00Z&to=2026-10-11T12:00:00Z"} {
		request("GET", "/api/admin/audits?"+query, nil, token, 400)
	}
	id, _ = request("POST", "/api/admin/password", map[string]string{"old_password": "initial-password", "new_password": "admin-new-secret"}, token, 200)
	check(id, "auth.password", "admin", "success", 200)
}

func TestAuditStorageFailureBlocksOperationsAndRetainsPendingActor(t *testing.T) {
	dsn := testutil.Database(t)
	if err := storage.Migrate("postgres", dsn, false); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secret := "0123456789abcdef0123456789abcdef"
	token := adminToken(t, store, secret)
	app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	request := func(name string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://confhub.test/api/admin/namespaces", strings.NewReader(`{"name":"`+name+`"}`))
		r.Header.Set("Origin", "http://confhub.test")
		r.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec("ALTER TABLE audit_logs RENAME TO unavailable_audits")
	w := request("blocked")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	exec("ALTER TABLE unavailable_audits RENAME TO audit_logs")
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM namespaces WHERE name='blocked'").Scan(&count); err != nil || count != 0 {
		t.Fatal("operation escaped audit failure", count, err)
	}
	// Only completion writes fail; actor persistence and the operation can commit.
	exec(`CREATE FUNCTION reject_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.result <> 'pending' THEN RAISE EXCEPTION 'test completion failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_completion BEFORE UPDATE ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_completion()`)
	w = request("committed")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM namespaces WHERE name='committed'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	page, err := store.Audits(context.Background(), storage.AuditFilter{Limit: 25, Result: "pending"})
	if err != nil || len(page.Audits) != 1 || page.Audits[0].Username != "admin" || page.Audits[0].UserID == "" || page.Audits[0].RequestID != w.Header().Get("X-Request-ID") {
		t.Fatal("interrupted operation lost actor", page, err)
	}
	exec("DROP TRIGGER reject_completion ON audit_logs; DROP FUNCTION reject_completion()")
	// Failure to persist the verified actor must also stop the handler.
	exec(`CREATE FUNCTION reject_actor() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.username <> '' THEN RAISE EXCEPTION 'test actor failure'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_actor BEFORE UPDATE ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_actor()`)
	w = request("actor-blocked")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM namespaces WHERE name='actor-blocked'").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
