package confhub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	confhub "github.com/vincent-vinf/confhub/sdk/go"
)

// This test uses only public management and SDK interfaces. The runner starts
// two real ConfHub processes sharing a disposable PostgreSQL database.
func TestRealServerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("real-server integration is outside the unit suite")
	}
	address := os.Getenv("CONFHUB_SDK_TEST_URL")
	if address == "" {
		t.Skip("run sdk/test-integration.py to start an isolated real server")
	}
	watchAddress := os.Getenv("CONFHUB_SDK_TEST_URL2")
	jar, _ := cookiejar.New(nil)
	admin := &http.Client{Jar: jar, Timeout: 3 * time.Second}
	request := func(method, path string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, address+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", address)
		response, err := admin.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("%s %s: HTTP %d", method, path, response.StatusCode)
		}
		var result map[string]any
		if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	request("POST", "/api/admin/login", map[string]string{"username": "admin", "password": os.Getenv("CONFHUB_SDK_TEST_PASSWORD")})
	key := confhub.Key{Namespace: "public", Group: "DEFAULT_GROUP", Name: fmt.Sprintf("go-sdk-%d", time.Now().UnixNano())}
	path := "/api/admin/namespaces/public/groups/DEFAULT_GROUP/configs/" + key.Name
	state := request("PUT", path, map[string]any{"content": "first\n", "format": "text", "confirmed": true})["state"].(map[string]any)
	client, err := confhub.New(confhub.Options{Addresses: []string{"http://127.0.0.1:1", watchAddress}, Tags: map[string]string{"env": "sdk"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	received := make(chan confhub.Snapshot, 10)
	if err = client.Subscribe(context.Background(), key, func(ctx context.Context, value confhub.Snapshot) { received <- value }); err != nil {
		t.Fatal(err)
	}
	next := func(version int64, deleted bool) confhub.Snapshot {
		t.Helper()
		select {
		case value := <-received:
			if value.Version != version || value.Deleted != deleted {
				t.Fatalf("unexpected callback %#v", value)
			}
			return value
		case <-time.After(5 * time.Second):
			t.Fatal("callback timeout")
			return confhub.Snapshot{}
		}
	}
	first := next(1, false)
	state = request("PUT", path, map[string]any{"expected_id": state["id"], "expected_revision": state["revision"], "content": "second\n", "format": "text", "confirmed": true})["state"].(map[string]any)
	next(2, false)
	state = request("PUT", path+"/rules", map[string]any{"expected_id": state["id"], "expected_revision": state["revision"], "confirmed": true, "source_version": 2, "rules": []any{map[string]any{"id": "gray", "name": "SDK", "enabled": true, "conditions": []any{map[string]any{"tag": "env", "operator": "eq", "values": []string{"sdk"}}}}}})["state"].(map[string]any)
	gray := next(0, false)
	if gray.RuleID != "gray" || !gray.Beta {
		t.Fatal("gray rule wasn't selected")
	}
	for _, content := range []string{"beta one", "beta two"} {
		state = request("PUT", path, map[string]any{"expected_id": state["id"], "expected_revision": state["revision"], "target": "beta", "content": content, "format": "text", "confirmed": true})["state"].(map[string]any)
		if value := next(0, false); value.Content != content || value.RuleID != "gray" || !value.Beta {
			t.Fatal("beta replacement not observed", value)
		}
	}

	request("DELETE", path, map[string]any{"expected_id": state["id"], "expected_revision": state["revision"], "confirmed": true})
	next(0, true)
	if _, err = client.Get(context.Background(), key); !errors.Is(err, confhub.ErrNotFound) {
		t.Fatal("deleted config retrievable", err)
	}
	request("PUT", path, map[string]any{"content": "rebuilt\n", "format": "text", "confirmed": true})
	rebuilt := next(1, false)
	if rebuilt.ID == first.ID || !strings.Contains(rebuilt.Content, "rebuilt") {
		t.Fatal("new identity not observed")
	}
	if err = client.Unsubscribe(key); err != nil {
		t.Fatal(err)
	}
}
