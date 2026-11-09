package server_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/vincent-vinf/confhub/internal/config"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vincent-vinf/confhub/internal/configarchive"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

func TestArchivePreviewCanCommitOnAnotherInstanceAndExport(t *testing.T) {
	store := testutil.Store(t)
	secret := "0123456789abcdef0123456789abcdef"
	makeServer := func() *httptest.Server {
		app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(app.Handler())
		t.Cleanup(ts.Close)
		return ts
	}
	a, b := makeServer(), makeServer()
	token := adminToken(t, store, secret)
	data, err := configarchive.Encode([]configarchive.Item{{Group: "IMPORTED", Name: "app.yaml", Content: "hello: 世界\r\n", Format: "yaml", Description: "说明"}})
	if err != nil {
		t.Fatal(err)
	}
	upload := func(base, path string, plan *storage.ImportPlan, auth bool) (int, []byte) {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		f, _ := w.CreateFormFile("file", "configs.zip")
		f.Write(data)
		w.WriteField("namespace", "public")
		w.WriteField("policy", "ABORT")
		if plan != nil {
			raw, _ := json.Marshal(plan)
			w.WriteField("plan", string(raw))
			w.WriteField("confirmed", "true")
		}
		w.Close()
		req, _ := http.NewRequest("POST", base+path, &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Origin", base)
		if auth {
			req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	path := "/api/admin/configs/import"
	if status, _ := upload(a.URL, path+"/preview", nil, false); status != 401 {
		t.Fatalf("unauthenticated %d", status)
	}
	status, raw := upload(a.URL, path+"/preview", nil, true)
	if status != 200 {
		t.Fatalf("preview %d %s", status, raw)
	}
	var plan storage.ImportPlan
	if err = json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.CanImport || len(plan.MissingGroups) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	status, raw = upload(b.URL, path, &plan, true)
	if status != 200 {
		t.Fatalf("commit %d %s", status, raw)
	}
	// Replaying the absent-name baseline must not publish a second version.
	if status, _ = upload(b.URL, path, &plan, true); status != 409 {
		t.Fatalf("replay %d", status)
	}
	req, _ := http.NewRequest("GET", a.URL+"/api/admin/configs/export?namespace=public&group=IMPORTED", nil)
	req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ = io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("export %d %s", res.StatusCode, raw)
	}
	p, err := configarchive.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].Content != "hello: 世界\r\n" || p.Items[0].Description != "说明" {
		t.Fatalf("export mismatch %+v", p.Items)
	}
}

func TestArchivePermissionsReadonlyAndAudit(t *testing.T) {
	store := testutil.Store(t)
	ctx := context.Background()
	secret := "0123456789abcdef0123456789abcdef"
	admin := adminToken(t, store, secret)
	app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	tokenFor := func(name, access string) string {
		t.Helper()
		u, e := store.CreateUser(ctx, name, "test-password", storage.UserInput{Enabled: true, Grants: []storage.Grant{{Namespace: "public", Group: "DEFAULT_GROUP", Access: access}}})
		if e != nil {
			t.Fatal(e)
		}
		token, e := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": u.ID, "iss": "confhub", "exp": time.Now().Add(time.Hour).Unix(), "auth_version": u.AuthVersion}).SignedString([]byte(secret))
		if e != nil {
			t.Fatal(e)
		}
		return token
	}
	reader, writer := tokenFor("reader", "read"), tokenFor("writer", "write")
	data, e := configarchive.Encode([]configarchive.Item{{Group: "DEFAULT_GROUP", Name: "allowed", Format: "text", Content: "sensitive-body-must-not-be-audited"}, {Group: "MISSING", Name: "forbidden", Format: "text", Content: "other"}})
	if e != nil {
		t.Fatal(e)
	}
	upload := func(base, path, token, origin string, data []byte, plan *storage.ImportPlan, want int) []byte {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		f, _ := w.CreateFormFile("file", "configs.zip")
		f.Write(data)
		w.WriteField("namespace", "public")
		w.WriteField("policy", "ABORT")
		if plan != nil {
			raw, _ := json.Marshal(plan)
			w.WriteField("plan", string(raw))
			w.WriteField("confirmed", "true")
		}
		w.Close()
		req, _ := http.NewRequest("POST", base+path, &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Origin", origin)
		cookieName := "confhub_admin"
		if base != ts.URL {
			cookieName = "confhub_readonly_admin"
		}
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d want %d: %s", path, res.StatusCode, want, raw)
		}
		return raw
	}
	preview := "/api/admin/configs/import/preview"
	upload(ts.URL, preview, reader, ts.URL, data, nil, 403)
	upload(ts.URL, preview, writer, ts.URL, data, nil, 403)
	upload(ts.URL, preview, admin, "https://other.invalid", data, nil, 403)
	var plan storage.ImportPlan
	decodeErr := json.Unmarshal(upload(ts.URL, preview, admin, ts.URL, data, nil, 200), &plan)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	// Losing per-group permission between preview and commit cannot partially import.
	upload(ts.URL, "/api/admin/configs/import", writer, ts.URL, data, &plan, 403)
	if _, e = store.Snapshot(ctx, config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "allowed"}); !errors.Is(e, config.ErrNotFound) {
		t.Fatal("forbidden batch partially committed", e)
	}
	upload(ts.URL, "/api/admin/configs/import", admin, ts.URL, data, &plan, 200)
	audits, e := store.Audits(ctx, storage.AuditFilter{Action: "config.import", Limit: 25})
	if e != nil {
		t.Fatal(e)
	}
	if len(audits.Audits) != 2 {
		t.Fatalf("import audit count %d", len(audits.Audits))
	}
	found := false
	for _, a := range audits.Audits {
		if a.Result == "success" {
			found = true
			if a.Username != "admin" || a.IP != "127.0.0.1" || !bytes.Contains(a.Details, []byte(`"created":2`)) || !bytes.Contains(a.Details, []byte(`"groups":["DEFAULT_GROUP","MISSING"]`)) {
				t.Fatalf("audit metadata %+v", a)
			}
		}
		if bytes.Contains(a.Details, []byte("sensitive-body")) {
			t.Fatal("body in audit")
		}
	}
	if !found {
		t.Fatal("missing success audit")
	}
	get := func(base, token, group string, want int) {
		t.Helper()
		req, _ := http.NewRequest("GET", base+"/api/admin/configs/export?namespace=public&group="+group, nil)
		cookieName := "confhub_admin"
		if base != ts.URL {
			cookieName = "confhub_readonly_admin"
		}
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("export %d want %d", res.StatusCode, want)
		}
	}
	get(ts.URL, reader, "DEFAULT_GROUP", 200)
	get(ts.URL, reader, "MISSING", 403)
	readonlyApp, e := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour, ReadOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	ro := httptest.NewServer(readonlyApp.Handler())
	defer ro.Close()
	get(ro.URL, reader, "DEFAULT_GROUP", 200)
	upload(ro.URL, preview, admin, ro.URL, data, nil, 403)
	upload(ro.URL, "/api/admin/configs/import", admin, ro.URL, data, &plan, 403)
}

func TestArchivePreviewReportsEveryInvalidItemAndMissingGroup(t *testing.T) {
	store := testutil.Store(t)
	secret := "0123456789abcdef0123456789abcdef"
	adminToken(t, store, secret)
	u, err := store.CreateUser(context.Background(), "writer", "test-password", storage.UserInput{Enabled: true, Grants: []storage.Grant{{Namespace: "public", Access: "write"}}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": u.ID, "iss": "confhub", "exp": time.Now().Add(time.Hour).Unix(), "auth_version": u.AuthVersion}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, value := range map[string]string{"DEFAULT_GROUP/bad-json": "{", "DEFAULT_GROUP/bad-type": "x", "MISSING/new": "x", "DEFAULT_GROUP/valid": "valid", ".metadata.yml": "metadata:\n- {group: DEFAULT_GROUP, dataId: bad-json, type: json}\n- {group: DEFAULT_GROUP, dataId: bad-type, type: unknown}\n- {group: MISSING, dataId: new, type: text}\n- {group: DEFAULT_GROUP, dataId: valid, type: text}\n"} {
		f, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = io.WriteString(f, value); e != nil {
			t.Fatal(e)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	upload := func(plan *storage.ImportPlan, want int) []byte {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		f, _ := w.CreateFormFile("file", "test.zip")
		f.Write(archive.Bytes())
		w.WriteField("namespace", "public")
		w.WriteField("policy", "ABORT")
		path := "/api/admin/configs/import/preview"
		if plan != nil {
			raw, _ := json.Marshal(plan)
			w.WriteField("plan", string(raw))
			w.WriteField("confirmed", "true")
			path = "/api/admin/configs/import"
		}
		w.Close()
		req, _ := http.NewRequest("POST", ts.URL+path, &body)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Origin", ts.URL)
		req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d want %d", path, res.StatusCode, want)
		}
		return raw
	}
	var plan storage.ImportPlan
	if err = json.Unmarshal(upload(nil, 200), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.CanImport || len(plan.Entries) != 4 {
		t.Fatalf("incomplete preview %+v", plan)
	}
	for i := 0; i < 3; i++ {
		if plan.Entries[i].Action != "error" || plan.Entries[i].Error == "" {
			t.Fatalf("missing error %+v", plan.Entries[i])
		}
	}
	if plan.Entries[3].Action != "create" {
		t.Fatal("valid row omitted")
	}
	upload(&plan, 400)
	if _, err = store.Snapshot(context.Background(), config.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: "valid"}); !errors.Is(err, config.ErrNotFound) {
		t.Fatal("invalid package wrote valid row")
	}
}
