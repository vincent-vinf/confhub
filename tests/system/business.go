package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

type pythonProbe struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Reader
	log     *os.File
	timeout time.Duration
}

func (r *runner) pythonProbe(tags map[string]string, label string) *pythonProbe {
	cmd := exec.CommandContext(r.ctx, r.python, r.worker)
	input, err := cmd.StdinPipe()
	must(err)
	output, err := cmd.StdoutPipe()
	must(err)
	log, err := os.OpenFile(filepath.Join(r.output, "python-"+label+".log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	must(err)
	cmd.Stderr = log
	must(cmd.Start())
	p := &pythonProbe{cmd: cmd, input: input, output: bufio.NewReader(output), log: log, timeout: r.timeout}
	initialized := false
	defer func() {
		if !initialized {
			p.close()
		}
	}()
	p.rpc(object{"op": "init", "addresses": r.addresses[1:], "tags": tags})
	initialized = true
	return p
}
func (p *pythonProbe) rpc(body object) json.RawMessage {
	raw, err := json.Marshal(body)
	must(err)
	_, err = p.input.Write(append(raw, '\n'))
	must(err)
	type reply struct {
		raw []byte
		err error
	}
	done := make(chan reply, 1)
	go func() { line, err := p.output.ReadBytes('\n'); done <- reply{line, err} }()
	select {
	case result := <-done:
		must(result.err)
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		decode(result.raw, &envelope)
		require(envelope.Error == "", "Python SDK: %s", envelope.Error)
		return envelope.Result
	case <-time.After(p.timeout):
		p.cmd.Process.Kill()
		panic(fmt.Errorf("Python worker response timeout"))
	}
}
func (p *pythonProbe) close() {
	p.input.Close()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		p.cmd.Process.Kill()
		<-done
	}
	p.log.Close()
}
func (p *pythonProbe) value(k sdk.Key) (sdk.Snapshot, int) {
	var out struct {
		Value sdk.Snapshot `json:"value"`
		Count int          `json:"count"`
	}
	decode(p.rpc(object{"op": "latest", "key": k}), &out)
	return out.Value, out.Count
}
func (r *runner) pythonObserved(p *pythonProbe, k sdk.Key, s state, content, ruleID string, v int64) {
	r.wait("Python SDK callback", func() bool { got, _ := p.value(k); return matches(got, s.ID, content, ruleID, v, false) })
	r.wait("Python SDK GET", func() bool {
		var out struct {
			Value  sdk.Snapshot `json:"value"`
			Source string       `json:"source"`
		}
		decode(p.rpc(object{"op": "get", "key": k}), &out)
		return matches(out.Value, s.ID, content, ruleID, v, false) && out.Source == "online"
	})
}
func (r *runner) business() {
	k := r.key("业务-配置.yaml")
	s := r.save(0, k, state{}, "first\n中文\\\"\t", "global").State
	normalTags := map[string]string{"env": "normal", "run": r.namespace, "sys.ip": "192.168.2.10"}
	grayTags := map[string]string{"env": "gray", "run": r.namespace, "sys.ip": "192.168.2.3"}
	normal := r.sdk([]string{r.addresses[1]}, normalTags, "")
	defer closeSDK(normal)
	gray := r.sdk([]string{r.addresses[len(r.addresses)-1]}, grayTags, "")
	defer closeSDK(gray)
	no, goObserver := newObserver(), newObserver()
	must(normal.Subscribe(r.ctx, k, no.callback))
	must(gray.Subscribe(r.ctx, k, goObserver.callback))
	pn := r.pythonProbe(normalTags, "normal")
	defer pn.close()
	pg := r.pythonProbe(grayTags, "gray")
	defer pg.close()
	pn.rpc(object{"op": "subscribe", "key": k})
	pg.rpc(object{"op": "subscribe", "key": k})
	check := func(normalContent, grayContent, ruleID string) {
		r.observed(no, k, s, normalContent, "", s.Global)
		r.get(normal, k, s, normalContent, "", s.Global)
		r.observed(goObserver, k, s, grayContent, ruleID, func() int64 {
			if ruleID != "" {
				return 0
			}
			return s.Global
		}())
		r.get(gray, k, s, grayContent, ruleID, func() int64 {
			if ruleID != "" {
				return 0
			}
			return s.Global
		}())
		r.pythonObserved(pn, k, s, normalContent, "", s.Global)
		v := s.Global
		if ruleID != "" {
			v = 0
		}
		r.pythonObserved(pg, k, s, grayContent, ruleID, v)
	}
	check("first\n中文\\\"\t", "first\n中文\\\"\t", "")
	// Same names in a different group must not affect subscriptions or lookups.
	other := k
	other.Group = "other"
	r.keys = append(r.keys, other)
	otherState := r.save(1, other, state{}, "other-group", "").State
	r.get(normal, other, otherState, "other-group", "", 1)
	s = r.save(0, k, s, "second", "").State
	check("second", "second", "")
	rules := grayRules()
	s = r.rules(k, s, rules, 1)
	check("second", "first\n中文\\\"\t", "specific")
	for _, content := range []string{"beta one", "beta two"} {
		_, count := no.value(k)
		_, pyCount := pn.value(k)
		s = r.save(1, k, s, content, "beta").State
		check("second", content, "specific")
		r.unchanged(no, k, count)
		_, afterCount := pn.value(k)
		require(afterCount == pyCount, "Python unaffected client received update")
		require(s.Last == 2, "beta consumed main version")
	}
	// Metadata-only beta updates must not trigger any client callback.
	_, count := goObserver.value(k)
	_, pyCount := pg.value(k)
	body := editBody(s, "beta two", "beta")
	body["description"] = "description only"
	var m mutation
	r.call(0, "PUT", configPath(k), body, &m)
	require(m.Changed, "beta description not stored")
	s = m.State
	r.unchanged(goObserver, k, count)
	_, newCount := pg.value(k)
	require(pyCount == newCount, "description caused Python callback")
	// First matching rule changes after a reorder while beta content remains shared.
	rules = []rule{rules[1], rules[0]}
	s = r.rules(k, s, rules, 0)
	check("second", "beta two", "fallback")
	rules[0].Enabled = false
	s = r.rules(k, s, rules, 0)
	check("second", "beta two", "specific")
	rules[1].Enabled = false
	s = r.rules(k, s, rules, 0)
	check("second", "second", "")
	require(s.Beta != nil, "disabled rules removed beta")
	rules[1].Enabled = true
	s = r.rules(k, s, rules, 0)
	check("second", "beta two", "specific")
	_, count = no.value(k)
	unchanged := r.save(0, k, s, "second", "")
	require(!unchanged.Changed && unchanged.Sequence == 0 && unchanged.State.Last == 2, "no-op created version")
	r.unchanged(no, k, count)
	s = r.save(0, k, s, "third", "").State
	check("third", "beta two", "specific")
	s = r.copy(k, s, "rollback", 1)
	require(s.Last == 4 && s.Beta.Content == "beta two", "rollback changed beta or numbering")
	check("first\n中文\\\"\t", "beta two", "specific")
	var restored version
	r.call(0, "GET", configPath(k)+"/versions/4", nil, &restored)
	require(restored.Source == 1 && restored.Description != "", "rollback lost source description")
	s = r.copy(k, s, "promote", 0)
	require(s.Last == 5 && len(s.Rules) == 2 && s.Beta != nil, "promotion removed beta/rules")
	check("beta two", "beta two", "specific")
	var history struct {
		Versions []version `json:"versions"`
		Before   int64     `json:"next_before"`
	}
	r.call(0, "GET", configPath(k)+"/versions?limit=2", nil, &history)
	require(len(history.Versions) == 2 && history.Versions[0].Number == 5 && history.Before == 4, "history pagination failed")
	r.call(0, "GET", configPath(k)+"/versions?limit=2&before=4", nil, &history)
	require(len(history.Versions) == 2 && history.Versions[0].Number == 3, "history cursor failed")
	s = r.rules(k, s, []rule{}, 0)
	require(s.Beta == nil, "last rule deletion left beta")
	check("beta two", "beta two", "")
	s = r.rules(k, s, grayRules(), 2)
	check("beta two", "second", "specific")
	// Simulated and GET routing must agree for inclusive IP boundaries and literal values.
	for _, tc := range []struct{ ip, env, wantRule, wantContent string }{{"192.168.2.1", "canary", "specific", "second"}, {"192.168.2.5", "canary", "specific", "second"}, {"192.168.2.6", "canary", "", "beta two"}, {"bad", "canary", "", "beta two"}, {"192.168.2.3", "GRAY", "", "beta two"}} {
		var simulated sdk.Snapshot
		r.call(0, "POST", configPath(k)+"/simulate", object{"tags": map[string]string{"env": tc.env, "sys.ip": tc.ip}}, &simulated)
		v := s.Global
		if tc.wantRule != "" {
			v = 0
		}
		require(matches(simulated, s.ID, tc.wantContent, tc.wantRule, v, false), "simulation mismatch for %s", tc.ip)
		c := r.sdk(r.addresses, map[string]string{"env": tc.env, "sys.ip": tc.ip}, "")
		func() { defer closeSDK(c); r.get(c, k, s, tc.wantContent, tc.wantRule, v) }()
	}
	id := s.ID
	r.status(0, "DELETE", configPath(k), baseline(s), 200)
	r.wait("Go deletion", func() bool { a, _ := no.value(k); b, _ := goObserver.value(k); return a.Deleted && b.Deleted })
	for _, p := range []*pythonProbe{pn, pg} {
		r.wait("Python deletion", func() bool { v, _ := p.value(k); return v.Deleted })
		var out struct {
			NotFound bool `json:"not_found"`
		}
		decode(p.rpc(object{"op": "get", "key": k}), &out)
		require(out.NotFound, "Python deletion retrievable")
	}
	s = r.save(2%len(r.addresses), k, state{}, "rebuilt", "").State
	require(s.ID != id && s.Last == 1, "recreation identity/number wrong")
	check("rebuilt", "rebuilt", "")
	r.status(0, "PUT", configPath(k), editBody(state{ID: id, Revision: 1}, "stale", ""), 409)
	for _, p := range []*pythonProbe{pn, pg} {
		p.rpc(object{"op": "unsubscribe", "key": k})
	}
	must(normal.Unsubscribe(k))
	must(gray.Unsubscribe(k))
}
