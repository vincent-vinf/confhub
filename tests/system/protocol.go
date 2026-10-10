package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

func (r *runner) protocol() {
	k := r.key("protocol #中文.json")
	s := r.save(0, k, state{}, "first", "").State
	for _, tc := range []struct {
		method, path string
		body         object
		status       int
	}{
		{"PUT", configPath(k), object{"content": "unconfirmed", "format": "text"}, 400},
		{"PUT", configPath(k), object{"confirmed": true, "content": "{bad", "format": "json"}, 400},
		{"PUT", configPath(k), object{"confirmed": true, "content": "bad", "format": "unknown"}, 400},
		{"PUT", configPath(k), object{"confirmed": true, "content": "bad", "format": "text", "rule_id": "retired"}, 400},
		{"GET", configPath(k) + "/versions?limit=101", nil, 400}, {"GET", configPath(k) + "/versions?before=bad", nil, 400}, {"GET", configPath(k) + "/versions/0", nil, 400}, {"GET", configPath(k) + "/versions/999", nil, 404},
		{"GET", "/api/admin/clients?limit=0", nil, 400}, {"GET", "/api/admin/clients?limit=bad", nil, 400},
		{"DELETE", "/api/admin/namespaces/" + r.namespace, object{"confirmed": true}, 400},
		{"DELETE", "/api/admin/namespaces/" + r.namespace + "/groups/cases", object{"confirmed": true}, 400},
		{"GET", "/api/unknown", nil, 404}, {"GET", "/missing.js", nil, 404},
	} {
		r.status(0, tc.method, tc.path, tc.body, tc.status)
	}
	final := r.read(k)
	require(final.Revision == s.Revision && final.Last == 1 && final.Versions[1].Content == "first", "invalid requests mutated state")
	// Anonymous client GET and rejected anonymous management requests.
	for _, tc := range []struct {
		path   string
		status int
	}{{configPath(k), 401}, {"/api/client/config?namespace=" + r.namespace + "&group=cases&name=" + url.QueryEscape(k.Name), 200}, {"/api/client/config?name=missing&namespace=" + r.namespace + "&group=cases", 404}, {"/api/client/config?tags=not-json&name=x", 400}} {
		res, err := http.Get(r.addresses[0] + tc.path)
		must(err)
		res.Body.Close()
		require(res.StatusCode == tc.status, "anonymous %s got %d", tc.path, res.StatusCode)
	}
	// Cross-origin management mutation must fail without changing namespace data.
	req, err := http.NewRequest("POST", r.addresses[0]+"/api/admin/namespaces", strings.NewReader(`{"name":"unexpected"}`))
	must(err)
	req.Header.Set("Origin", "https://unrelated.invalid")
	req.Header.Set("Content-Type", "application/json")
	res, err := r.admins[0].Do(req)
	must(err)
	res.Body.Close()
	require(res.StatusCode == 403, "cross-origin mutation accepted")
	// Pagination scans only this run's group and preserves literal names.
	var rows []struct {
		Key sdk.Key `json:"key"`
	}
	r.call(0, "GET", "/api/admin/namespaces/"+r.namespace+"/groups/cases/configs?limit=1&after="+url.QueryEscape(k.Name), nil, &rows)
	for _, row := range rows {
		require(row.Key.Name > k.Name, "list cursor returned previous name")
	}
	endpoint := "ws" + strings.TrimPrefix(r.addresses[1], "http") + "/api/client/watch"
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	must(err)
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(r.timeout))
	must(conn.WriteJSON(object{"op": "subscribe", "key": k}))
	var snapshot sdk.Snapshot
	must(conn.ReadJSON(&snapshot))
	require(matches(snapshot, s.ID, "first", "", 1, false), "raw protocol snapshot mismatch")
	must(conn.WriteJSON(object{"op": "unknown", "key": k}))
	_, _, err = conn.ReadMessage()
	require(websocket.IsCloseError(err, websocket.ClosePolicyViolation), "invalid operation did not close with policy violation")
	// Ten subscriptions fit; releasing one slot admits another, the eleventh closes.
	conn2, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	must(err)
	defer conn2.Close()
	conn2.SetReadDeadline(time.Now().Add(r.timeout))
	keys := []sdk.Key{}
	for i := 0; i < 10; i++ {
		key := r.key(fmt.Sprintf("protocol-slot-%d", i))
		keys = append(keys, key)
		must(conn2.WriteJSON(object{"op": "subscribe", "key": key}))
		must(conn2.ReadJSON(&snapshot))
		require(snapshot.Deleted, "missing subscription not marked deleted")
	}
	must(conn2.WriteJSON(object{"op": "unsubscribe", "key": keys[0]}))
	extra := r.key("protocol-extra")
	must(conn2.WriteJSON(object{"op": "subscribe", "key": extra}))
	must(conn2.ReadJSON(&snapshot))
	require(snapshot.Key == extra, "released slot was not reusable")
	must(conn2.WriteJSON(object{"op": "subscribe", "key": r.key("protocol-overflow")}))
	_, _, err = conn2.ReadMessage()
	require(websocket.IsCloseError(err, websocket.ClosePolicyViolation), "subscription overflow accepted")
}
