package main

import (
	"context"
	"fmt"
	"sync"

	sdk "gitlab.bodesitech.com/bodesi/confhub/sdk/go"
)

type operation struct {
	method, path string
	body         object
}
type response struct {
	status int
	raw    []byte
	err    error
}

func (r *runner) race(ops []operation) []response {
	out := make([]response, len(ops))
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(len(ops))
	done.Add(len(ops))
	for i, op := range ops {
		go func(i int, op operation) {
			defer done.Done()
			ready.Done()
			<-start
			out[i].status, out[i].raw, out[i].err = r.request(i%len(r.addresses), op.method, op.path, op.body)
		}(i, op)
	}
	ready.Wait()
	close(start)
	done.Wait()
	for _, res := range out {
		must(res.err)
	}
	return out
}
func winner(results []response) int {
	index := -1
	for i, res := range results {
		if res.status == 200 {
			require(index == -1, "multiple writers succeeded")
			index = i
		} else {
			require(res.status == 409, "expected conflict, got %d", res.status)
		}
	}
	require(index >= 0, "all writers failed")
	return index
}
func (r *runner) concurrency() {
	for round := 0; round < r.rounds; round++ {
		for _, target := range []string{"global", "beta"} {
			k := r.key(fmt.Sprintf("race-%s-%d", target, round))
			s := r.save(0, k, state{}, "initial", "").State
			if target == "beta" {
				s = r.rules(k, s, grayRules(), 1)
			}
			observer := newObserver()
			c := r.sdk(r.addresses, map[string]string{"env": "gray", "sys.ip": "192.168.2.3"}, "")
			func() {
				defer closeSDK(c)
				must(c.Subscribe(r.ctx, k, observer.callback))
				ruleID := ""
				v := int64(1)
				if target == "beta" {
					ruleID = "specific"
					v = 0
				}
				r.observed(observer, k, s, "initial", ruleID, v)
				ops := make([]operation, r.writers)
				for i := range ops {
					ops[i] = operation{"PUT", configPath(k), editBody(s, fmt.Sprintf("writer-%d", i), target)}
				}
				index := winner(r.race(ops))
				final := r.read(k)
				expected := fmt.Sprintf("writer-%d", index)
				require(final.Revision == s.Revision+1, "race consumed extra revisions")
				if target == "beta" {
					require(final.Last == 1 && final.Beta.Content == expected, "beta race corrupted state")
				} else {
					require(final.Last == 2 && final.Versions[2].Content == expected, "main race corrupted state")
					v = 2
				}
				r.observed(observer, k, final, expected, ruleID, v)
				var history struct {
					Versions []version `json:"versions"`
				}
				r.call(0, "GET", configPath(k)+"/versions", nil, &history)
				require(int64(len(history.Versions)) == final.Last, "failed writer leaked history")
			}()
		}
		for _, kind := range []string{"main-rules", "beta-remove", "delete-edit"} {
			k := r.key(fmt.Sprintf("mixed-%s-%d", kind, round))
			s := r.save(0, k, state{}, "initial", "").State
			if kind == "beta-remove" {
				s = r.rules(k, s, grayRules(), 1)
			}
			first := operation{"PUT", configPath(k), editBody(s, "edited", "")}
			second := operation{"DELETE", configPath(k), baseline(s)}
			if kind != "delete-edit" {
				b := baseline(s)
				b["rules"] = grayRules()
				b["source_version"] = 1
				if kind == "beta-remove" {
					first.body = editBody(s, "edited", "beta")
					b["rules"] = []rule{}
					b["source_version"] = 0
				}
				second = operation{"PUT", configPath(k) + "/rules", b}
			}
			index := winner(r.race([]operation{first, second}))
			if kind == "delete-edit" && index == 1 {
				r.status(0, "GET", configPath(k), nil, 404)
				rebuilt := r.save(0, k, state{}, "rebuilt", "").State
				require(rebuilt.ID != s.ID, "rebuild reused identity")
				r.status(0, "PUT", configPath(k), editBody(s, "late", ""), 409)
				continue
			}
			final := r.read(k)
			require(final.Revision == s.Revision+1, "mixed race lost revision")
			switch kind {
			case "main-rules":
				if index == 0 {
					require(final.Beta == nil && final.Last == 2, "rules loser leaked state")
				} else {
					require(final.Beta != nil && final.Beta.Content == "initial" && final.Last == 1, "main loser leaked state")
				}
			case "beta-remove":
				if index == 0 {
					require(final.Beta != nil && final.Beta.Content == "edited" && len(final.Rules) == 2, "beta winner lost state")
				} else {
					require(final.Beta == nil && len(final.Rules) == 0, "rule removal left beta")
				}
			case "delete-edit":
				require(final.Versions[2].Content == "edited", "edit winner lost content")
			}
		}
	}
	r.fanout()
}
func (r *runner) fanout() {
	keys := make([]sdk.Key, r.configs)
	states := make([]state, r.configs)
	for i := range keys {
		keys[i] = r.key(fmt.Sprintf("fanout-%d", i))
		states[i] = r.save(i, keys[i], state{}, fmt.Sprintf("initial-%d", i), "").State
	}
	observers := make([]*observer, r.clients)
	clients := make([]*sdk.Client, r.clients)
	defer func() {
		for _, c := range clients {
			if c != nil {
				closeSDK(c)
			}
		}
	}()
	for i := range clients {
		clients[i] = r.sdk([]string{r.addresses[i%len(r.addresses)]}, map[string]string{"run": r.namespace}, "")
		observers[i] = newObserver()
		k := keys[i%len(keys)]
		must(clients[i].Subscribe(r.ctx, k, observers[i].callback))
		r.observed(observers[i], k, states[i%len(keys)], fmt.Sprintf("initial-%d", i%len(keys)), "", 1)
	}
	slow := r.sdk(r.addresses, nil, "")
	defer closeSDK(slow)
	slowObserver := newObserver()
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	must(slow.Subscribe(r.ctx, keys[0], func(ctx context.Context, s sdk.Snapshot) {
		select {
		case <-release:
			slowObserver.callback(ctx, s)
		case <-ctx.Done():
		}
	}))
	// Publish different keys together; do not require every intermediate notification.
	for round := 0; round < r.rounds; round++ {
		ops := make([]operation, len(keys))
		for i, k := range keys {
			ops[i] = operation{"PUT", configPath(k), editBody(states[i], fmt.Sprintf("config-%d-round-%d", i, round), "")}
		}
		responses := r.race(ops)
		for i, res := range responses {
			require(res.status == 200, "independent config publish failed HTTP %d", res.status)
			var m mutation
			decode(res.raw, &m)
			states[i] = m.State
		}
	}
	for i, o := range observers {
		index := i % len(keys)
		r.observed(o, keys[index], states[index], fmt.Sprintf("config-%d-round-%d", index, r.rounds-1), "", int64(r.rounds+1))
	}
	close(release)
	released = true
	r.observed(slowObserver, keys[0], states[0], fmt.Sprintf("config-0-round-%d", r.rounds-1), "", int64(r.rounds+1))
	// Exercise multiplexed subscriptions in Python on the same final states.
	p := r.pythonProbe(map[string]string{"run": r.namespace}, "fanout")
	defer p.close()
	for i, k := range keys {
		p.rpc(object{"op": "subscribe", "key": k})
		r.pythonObserved(p, k, states[i], fmt.Sprintf("config-%d-round-%d", i, r.rounds-1), "", int64(r.rounds+1))
	}
}
