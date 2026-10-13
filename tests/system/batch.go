package main

import (
	"fmt"
	"maps"
	"reflect"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

func batchBody(keys []sdk.Key, states []state, group string) object {
	require(len(keys) > 0 && len(keys) == len(states), "invalid batch fixture")
	targets := make([]object, len(keys))
	for i, k := range keys {
		targets[i] = object{"key": k, "expected_id": states[i].ID, "expected_revision": states[i].Revision}
	}
	b := object{"configs": targets, "confirmed": true}
	if group != "" {
		b["namespace"], b["group"] = keys[0].Namespace, group
	}
	return b
}
func requireMoved(before, after state, key sdk.Key) {
	require(after.Key == key && after.ID == before.ID && after.Revision == before.Revision+1, "move changed identity, key or revision incorrectly")
	require(after.Global == before.Global && after.Last == before.Last && reflect.DeepEqual(after.Versions, before.Versions) && reflect.DeepEqual(after.Rules, before.Rules) && reflect.DeepEqual(after.Beta, before.Beta), "move changed versions, history, beta or rules")
}
func (r *runner) deleted(o *observer, k sdk.Key, id string) {
	r.wait("Go SDK deletion "+k.Name, func() bool { value, _ := o.value(k); return value.Deleted && value.ID == id })
}
func (r *runner) pythonDeleted(p *pythonProbe, k sdk.Key, id string) {
	var value sdk.Snapshot
	var count int
	defer func() {
		if err := recover(); err != nil {
			panic(fmt.Errorf("%v; last deleted=%t id=%q sequence=%d callbacks=%d expected_id=%q", err, value.Deleted, value.ID, value.Sequence, count, id))
		}
	}()
	r.wait("Python SDK deletion "+k.Name, func() bool {
		value, count = p.value(k)
		return value.Deleted && value.ID == id
	})
}

func (r *runner) batch() {
	normalTags := map[string]string{"run": r.namespace, "env": "normal", "probe": "go-normal"}
	grayTags := map[string]string{"run": r.namespace, "env": "gray", "sys.ip": "192.168.2.3", "probe": "go-gray"}
	normal := r.sdk([]string{r.addresses[1]}, normalTags, "")
	defer closeSDK(normal)
	gray := r.sdk([]string{r.addresses[len(r.addresses)-1]}, grayTags, "")
	defer closeSDK(gray)
	no, goGray := newObserver(), newObserver()
	pythonNormalTags, pythonGrayTags := maps.Clone(normalTags), maps.Clone(grayTags)
	pythonNormalTags["probe"], pythonGrayTags["probe"] = "python-normal", "python-gray"
	pn := r.pythonProbe(pythonNormalTags, "batch-normal")
	defer pn.close()
	pg := r.pythonProbe(pythonGrayTags, "batch-gray")
	defer pg.close()
	subscribe := func(k sdk.Key) {
		must(normal.Subscribe(r.ctx, k, no.callback))
		must(gray.Subscribe(r.ctx, k, goGray.callback))
		pn.rpc(object{"op": "subscribe", "key": k})
		pg.rpc(object{"op": "subscribe", "key": k})
	}
	present := func(k sdk.Key, s state) {
		content := s.Versions[s.Global].Content
		grayContent, ruleID, grayVersion := content, "", s.Global
		if s.Beta != nil {
			grayContent, ruleID, grayVersion = s.Beta.Content, "specific", 0
		}
		r.observed(no, k, s, content, "", s.Global)
		r.observed(goGray, k, s, grayContent, ruleID, grayVersion)
		r.get(normal, k, s, content, "", s.Global)
		r.get(gray, k, s, grayContent, ruleID, grayVersion)
		r.pythonObserved(pn, k, s, content, "", s.Global)
		r.pythonObserved(pg, k, s, grayContent, ruleID, grayVersion)
	}
	removed := func(k sdk.Key, id string) {
		r.deleted(no, k, id)
		r.deleted(goGray, k, id)
		r.pythonDeleted(pn, k, id)
		r.pythonDeleted(pg, k, id)
	}
	ready := func(keys, destinations []sdk.Key, states []state) {
		// Initial SDK callbacks can come from GET. Wait for all four real
		// WebSocket sessions to send their source and target snapshots as well.
		r.wait("batch WebSocket subscriptions registered", func() bool {
			probes := map[string]bool{}
			for _, client := range r.online(0) {
				found := map[sdk.Key]string{}
				for _, sub := range client.Subscriptions {
					if sub.Sent {
						found[sub.Key] = sub.ID
					}
				}
				matched := true
				for i, key := range keys {
					id, targetSent := found[destinations[i]]
					matched = matched && found[key] == states[i].ID && targetSent && id == ""
				}
				if matched {
					probes[client.Tags["probe"]] = true
				}
			}
			return probes["go-normal"] && probes["go-gray"] && probes["python-normal"] && probes["python-gray"]
		})
	}
	keys := []sdk.Key{r.key("batch-a"), r.key("batch-b")}
	destinations := make([]sdk.Key, len(keys))
	states := make([]state, len(keys))
	for i, k := range keys {
		states[i] = r.save(0, k, state{}, fmt.Sprintf("main-one-%d", i), "").State
		states[i] = r.save(0, k, states[i], fmt.Sprintf("main-two-%d", i), "").State
		states[i] = r.rules(k, states[i], grayRules(), 1)
		states[i] = r.save(0, k, states[i], fmt.Sprintf("beta-%d", i), "beta").State
		destinations[i] = r.targetKey(k, "other")
		subscribe(k)
		subscribe(destinations[i])
		present(k, states[i])
		removed(destinations[i], "")
	}
	ready(keys, destinations, states)
	failed := func(body object) {
		before := make([][4]int, len(keys))
		for i, k := range keys {
			_, before[i][0] = no.value(k)
			_, before[i][1] = goGray.value(k)
			_, before[i][2] = pn.value(k)
			_, before[i][3] = pg.value(k)
		}
		r.status(0, "POST", "/api/admin/configs/batch-move", body, 409)
		for i, k := range keys {
			require(reflect.DeepEqual(r.read(k), states[i]), "failed batch changed a source configuration")
			r.unchanged(no, k, before[i][0])
			r.unchanged(goGray, k, before[i][1])
			_, pc := pn.value(k)
			_, gc := pg.value(k)
			require(pc == before[i][2] && gc == before[i][3], "failed batch notified Python subscribers")
		}
	}
	// The second item conflicts: the first item must remain untouched as well.
	r.save(1, destinations[1], state{}, "existing-target", "")
	collision := r.read(destinations[1])
	present(destinations[1], collision)
	failed(batchBody(keys, states, "other"))
	r.status(0, "GET", configPath(destinations[0]), nil, 404)
	require(reflect.DeepEqual(r.read(destinations[1]), collision), "batch overwrote the target collision")
	r.status(1, "DELETE", configPath(destinations[1]), baseline(collision), 200)
	removed(destinations[1], collision.ID)
	stale := append([]state(nil), states...)
	r.save(1, keys[1], states[1], "main-after-concurrent-edit", "")
	states[1] = r.read(keys[1])
	present(keys[1], states[1])
	failed(batchBody(keys, stale, "other"))
	for _, k := range destinations {
		r.status(1, "GET", configPath(k), nil, 404)
	}
	r.call(0, "POST", "/api/admin/configs/batch-move", batchBody(keys, states, "other"), nil)
	for i, k := range keys {
		r.status(1, "GET", configPath(k), nil, 404)
		moved := r.read(destinations[i])
		requireMoved(states[i], moved, destinations[i])
		removed(k, states[i].ID)
		present(destinations[i], moved)
		states[i] = moved
	}
	r.call(0, "POST", "/api/admin/configs/batch-delete", batchBody(destinations, states, ""), nil)
	for i, k := range destinations {
		r.status(1, "GET", configPath(k), nil, 404)
		removed(k, states[i].ID)
	}

	// Across actual endpoints, a whole-batch move and delete share one baseline.
	for round := 0; round < r.rounds; round++ {
		sources := []sdk.Key{r.key(fmt.Sprintf("batch-race-%d-a", round)), r.key(fmt.Sprintf("batch-race-%d-b", round))}
		targets := make([]sdk.Key, 2)
		snapshots := make([]state, 2)
		for i, k := range sources {
			r.save(i, k, state{}, fmt.Sprintf("race-%d-%d", round, i), "")
			snapshots[i] = r.read(k)
			targets[i] = r.targetKey(k, "other")
			subscribe(k)
			subscribe(targets[i])
			present(k, snapshots[i])
			removed(targets[i], "")
		}
		ready(sources, targets, snapshots)
		results := r.race([]operation{
			{"POST", "/api/admin/configs/batch-move", batchBody(sources, snapshots, "other")},
			{"POST", "/api/admin/configs/batch-delete", batchBody(sources, snapshots, "")},
		})
		index := -1
		for i, res := range results {
			if res.status == 200 {
				require(index == -1, "both batch move and delete succeeded")
				index = i
			} else {
				require(res.status == 404, "batch loser expected missing source, got %d", res.status)
			}
		}
		require(index >= 0, "both batch move and delete failed")
		for i, k := range sources {
			r.status(1, "GET", configPath(k), nil, 404)
			removed(k, snapshots[i].ID)
			if index == 0 {
				moved := r.read(targets[i])
				requireMoved(snapshots[i], moved, targets[i])
				present(targets[i], moved)
			} else {
				r.status(1, "GET", configPath(targets[i]), nil, 404)
				removed(targets[i], "")
			}
		}
	}
}
