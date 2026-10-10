package main

import (
	"net/url"

	sdk "gitlab.bodesitech.com/bodesi/confhub/sdk/go"
)

type onlineClient struct {
	ID            string            `json:"id"`
	Instance      string            `json:"instance_id"`
	Tags          map[string]string `json:"tags"`
	Subscriptions []struct {
		Key     sdk.Key `json:"key"`
		ID      string  `json:"id"`
		Version int64   `json:"version"`
		Beta    bool    `json:"beta"`
		RuleID  string  `json:"rule_id"`
		Sent    bool    `json:"sent"`
	} `json:"subscriptions"`
}

func (r *runner) online(index int) []onlineClient {
	all := []onlineClient{}
	after := ""
	for {
		var page struct {
			Clients []onlineClient `json:"clients"`
			Next    string         `json:"next_after"`
		}
		r.call(index, "GET", "/api/admin/clients?limit=25&after="+url.QueryEscape(after), nil, &page)
		for _, c := range page.Clients {
			if c.Tags["run"] == r.namespace {
				all = append(all, c)
			}
		}
		if page.Next == "" {
			return all
		}
		require(page.Next != after, "client pagination stalled")
		after = page.Next
	}
}
func (r *runner) presence() {
	r.wait("previous clients disconnected", func() bool { return len(r.online(0)) == 0 })
	k := r.key("presence")
	s := r.save(0, k, state{}, "online", "").State
	tags := map[string]string{"run": r.namespace, "suggest": "literal_% 中文 ", "sys.hostname": "system-host"}
	c := r.sdk([]string{r.addresses[1]}, tags, "")
	closed := false
	defer func() {
		if !closed {
			closeSDK(c)
		}
	}()
	r.get(c, k, s, "online", "", 1)
	require(len(r.online(0)) == 0, "HTTP GET counted as connection")
	observer := newObserver()
	must(c.Subscribe(r.ctx, k, observer.callback))
	r.observed(observer, k, s, "online", "", 1)
	r.wait("cross-instance presence", func() bool {
		for i := range r.addresses {
			rows := r.online(i)
			if len(rows) != 1 || len(rows[0].Subscriptions) != 1 || !rows[0].Subscriptions[0].Sent || rows[0].Subscriptions[0].ID != s.ID || rows[0].Subscriptions[0].Version != 1 {
				return false
			}
		}
		return true
	})
	var names, values []string
	r.call(0, "GET", "/api/admin/client-tags?prefix=sys.", nil, &names)
	has := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	require(has(names, "sys.ip") && has(names, "sys.hostname"), "built-in tags absent")
	r.call(0, "GET", "/api/admin/client-tags?tag=suggest&prefix="+url.QueryEscape("literal_%"), nil, &values)
	require(has(values, tags["suggest"]), "literal suggestion changed")
	s = r.rules(k, s, grayRules(), 1)
	s = r.save(0, k, s, "beta", "beta").State
	r.get(c, k, s, "online", "", 1)
	closeSDK(c)
	closed = true
	r.wait("disconnected presence removed", func() bool { return len(r.online(0)) == 0 })
}
