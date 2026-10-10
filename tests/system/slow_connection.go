package main

import (
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// A small TCP receive buffer and an unread websocket exercise the real server's
// write deadline. Waiting for healthy SDK callbacks separates successive writes
// without relying on a sleep to manufacture the race.
func (r *runner) slowConnection() {
	k := r.key("slow-wire")
	s := r.save(0, k, state{}, "warmup", "").State
	c := r.sdk([]string{r.addresses[1]}, nil, "")
	defer closeSDK(c)
	o := newObserver()
	must(c.Subscribe(r.ctx, k, o.callback))
	r.observed(o, k, s, "warmup", "", 1)
	raw, _ := json.Marshal(map[string]string{"run": r.namespace, "kind": "slow-wire"})
	endpoint := "ws" + strings.TrimPrefix(r.addresses[0], "http") + "/api/client/watch?tags=" + url.QueryEscape(string(raw))
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	must(err)
	defer conn.Close()
	tcp, ok := conn.UnderlyingConn().(*net.TCPConn)
	require(ok, "slow wire test requires plain TCP in isolated mode")
	must(tcp.SetReadBuffer(1024))
	must(conn.WriteJSON(object{"op": "subscribe", "key": k}))
	conn.SetReadDeadline(time.Now().Add(r.timeout))
	_, _, err = conn.ReadMessage()
	must(err)
	r.wait("slow socket registered", func() bool {
		for _, row := range r.online(1) {
			if row.Tags["kind"] == "slow-wire" {
				return true
			}
		}
		return false
	})
	for i := 0; i < 12; i++ {
		content := strings.Repeat(string(rune('a'+i)), 1<<20)
		s = r.save(0, k, s, content, "").State
		r.observed(o, k, s, content, "", s.Global)
	}
	deadline := time.Now().Add(15 * time.Second)
	r.wait("slow writer removed before heartbeat timeout", func() bool {
		require(time.Now().Before(deadline), "slow writer was not closed within bounded write deadline and presence refresh")
		for _, row := range r.online(1) {
			if row.Tags["kind"] == "slow-wire" {
				return false
			}
		}
		return true
	})
	s = r.save(0, k, s, "healthy-after-slow-close", "").State
	r.observed(o, k, s, "healthy-after-slow-close", "", s.Global)
}
