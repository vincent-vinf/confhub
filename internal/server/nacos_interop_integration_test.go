package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/testutil"
)

// This must point at a disposable, authentication-disabled Nacos 2.5.1 instance.
func TestRealNacosNewZIPInteroperability(t *testing.T) {
	nacos := strings.TrimRight(os.Getenv("CONFHUB_TEST_NACOS_URL"), "/")
	if nacos == "" {
		t.Skip("run tests/nacos-interop.sh for real Nacos interoperability")
	}
	store := testutil.Store(t)
	secret := "0123456789abcdef0123456789abcdef"
	token := adminToken(t, store, secret)
	app, err := server.New(store, nil, server.Options{JWTSecret: secret, JWTExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	defer ts.Close()
	group := "interop-" + uuid.NewString()
	type fixture struct{ name, format, content, description string }
	fixtures := []fixture{{"noextension", "text", "中文 text\r\n\r\n", "说明"}, {"app.json", "json", "{\"value\": 1}\r\n", "json description"}, {"app.yaml", "yaml", "# original\r\nvalue: 1\r\n", "yaml description"}, {"app.toml", "toml", "value = 1\n", "toml"}, {"app.xml", "xml", "<value>1</value>\n", "xml"}, {"app.properties", "properties", "value=1\n", "properties"}}
	client := &http.Client{Timeout: 15 * time.Second}
	send := func(req *http.Request) []byte {
		t.Helper()
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != 200 {
			t.Fatalf("%s status %d error %v", req.URL.Path, res.StatusCode, e)
		}
		return raw
	}
	remove := func(name string) {
		t.Helper()
		q := url.Values{"dataId": {name}, "group": {group}}
		req, _ := http.NewRequest("DELETE", nacos+"/nacos/v1/cs/configs?"+q.Encode(), nil)
		send(req)
	}
	for _, f := range fixtures {
		form := url.Values{"dataId": {f.name}, "group": {group}, "content": {f.content}, "type": {f.format}, "desc": {f.description}}
		req, _ := http.NewRequest("POST", nacos+"/nacos/v1/cs/configs", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if string(send(req)) != "true" {
			t.Fatal("Nacos publish failed")
		}
		t.Cleanup(func() { remove(f.name) })
	}
	req, _ := http.NewRequest("GET", nacos+"/nacos/v1/cs/configs?"+url.Values{"exportV2": {"true"}, "group": {group}, "ids": {""}}.Encode(), nil)
	zipData := send(req)
	upload := func(path string, data []byte, fields map[string]string, confhub bool) []byte {
		t.Helper()
		var out bytes.Buffer
		w := multipart.NewWriter(&out)
		f, e := w.CreateFormFile("file", "synthetic.zip")
		if e != nil {
			t.Fatal(e)
		}
		f.Write(data)
		for k, v := range fields {
			w.WriteField(k, v)
		}
		w.Close()
		req, _ := http.NewRequest("POST", path, &out)
		req.Header.Set("Content-Type", w.FormDataContentType())
		if confhub {
			req.Header.Set("Origin", ts.URL)
			req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
		}
		return send(req)
	}
	fields := map[string]string{"namespace": "public", "policy": "ABORT"}
	raw := upload(ts.URL+"/api/admin/configs/import/preview", zipData, fields, true)
	var plan storage.ImportPlan
	if err = json.Unmarshal(raw, &plan); err != nil || !plan.CanImport {
		t.Fatal("preview", err)
	}
	encoded, _ := json.Marshal(plan)
	fields["plan"], fields["confirmed"] = string(encoded), "true"
	upload(ts.URL+"/api/admin/configs/import", zipData, fields, true)
	for _, f := range fixtures {
		state, e := store.Snapshot(context.Background(), config.Key{Namespace: "public", Group: group, Name: f.name})
		if e != nil {
			t.Fatal(e)
		}
		v := state.Versions[state.GlobalVersion]
		if v.Content != f.content || v.Format != f.format || v.Description != f.description {
			t.Fatalf("Nacos to ConfHub lost fields for %s", f.name)
		}
	}
	// Nacos's publish API coerces ini to text, but its ZIP importer preserves
	// declared ini metadata. Verify this outbound format separately.
	ini := fixture{"app.ini", "ini", "[s]\nvalue=1\n", "ini description"}
	if _, err = store.Save(context.Background(), config.Key{Namespace: "public", Group: group, Name: ini.name}, config.Edit{Content: ini.content, Format: ini.format, Description: ini.description}); err != nil {
		t.Fatal(err)
	}
	fixtures = append(fixtures, ini)
	t.Cleanup(func() { remove(ini.name) })
	req, _ = http.NewRequest("GET", ts.URL+"/api/admin/configs/export?"+url.Values{"namespace": {"public"}, "group": {group}}.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: "confhub_admin", Value: token})
	exported := send(req)
	for _, f := range fixtures {
		remove(f.name)
	}
	raw = upload(nacos+"/nacos/v1/cs/configs?import=true", exported, map[string]string{"namespace": "", "policy": "ABORT"}, false)
	var result struct {
		Code int `json:"code"`
		Data struct {
			Count int `json:"succCount"`
		} `json:"data"`
	}
	if err = json.Unmarshal(raw, &result); err != nil || result.Code != 200 || result.Data.Count != len(fixtures) {
		t.Fatalf("Nacos import result: %s error %v", raw, err)
	}
	for _, f := range fixtures {
		req, _ = http.NewRequest("GET", nacos+"/nacos/v1/cs/configs?"+url.Values{"show": {"all"}, "dataId": {f.name}, "group": {group}}.Encode(), nil)
		var v struct {
			Content string `json:"content"`
			Type    string `json:"type"`
			Desc    string `json:"desc"`
		}
		if err = json.Unmarshal(send(req), &v); err != nil {
			t.Fatal(err)
		}
		if v.Content != f.content || v.Type != f.format || v.Desc != f.description {
			t.Fatalf("ConfHub to Nacos lost fields for %s", f.name)
		}
	}
	t.Logf("Nacos 2.5.1 ↔ ConfHub: %d synthetic configurations, 6 inbound/7 outbound formats and metadata preserved", len(fixtures))
}
