package main

import (
	"fmt"
	"math/rand"

	sdk "github.com/vincent-vinf/confhub/sdk/go"
)

// Expected content comes from an independent operation ledger and literal tag
// fixtures; this program never imports the production routing implementation.
func (r *runner) random() {
	rng := rand.New(rand.NewSource(r.seed))
	k := r.key("random")
	s := r.save(0, k, state{}, "initial", "").State
	main := "initial"
	betaContent := ""
	last := int64(1)
	mainVersions := map[int64]string{1: main}
	rules := []rule{}
	tags := []map[string]string{{"env": "normal", "sys.ip": "192.168.2.3"}, {"env": "gray", "sys.ip": "192.168.2.3"}, {"env": "canary", "sys.ip": "192.168.2.8"}, {"env": "canary", "sys.ip": "192.168.2.1"}}
	clients := make([]*sdk.Client, len(tags))
	observers := make([]*observer, len(tags))
	defer func() {
		for _, c := range clients {
			if c != nil {
				closeSDK(c)
			}
		}
	}()
	for i, tags := range tags {
		clients[i] = r.sdk(r.addresses, tags, "")
		observers[i] = newObserver()
		must(clients[i].Subscribe(r.ctx, k, observers[i].callback))
	}
	for step := 0; step < 60; step++ {
		op := rng.Intn(7)
		content := fmt.Sprintf("seed-%d-step-%d", r.seed, step)
		switch op {
		case 0:
			s = r.save(step%len(r.addresses), k, s, content, "").State
			last++
			main = content
			mainVersions[last] = content
		case 1:
			if len(rules) == 0 {
				rules = grayRules()
				s = r.rules(k, s, rules, last)
				betaContent = main
			} else {
				s = r.save(1, k, s, content, "beta").State
				betaContent = content
			}
		case 2:
			if len(rules) > 0 {
				index := rng.Intn(len(rules))
				rules[index].Enabled = !rules[index].Enabled
				s = r.rules(k, s, rules, 0)
			}
		case 3:
			if len(rules) > 0 {
				rules[0], rules[1] = rules[1], rules[0]
				s = r.rules(k, s, rules, 0)
			}
		case 4:
			source := int64(rng.Intn(int(last)) + 1)
			selected := mainVersions[source]
			s = r.copy(k, s, "rollback", source)
			if selected != main {
				last++
				main = selected
				mainVersions[last] = main
			}
		case 5:
			if len(rules) > 0 {
				s = r.copy(k, s, "promote", 0)
				if main != betaContent {
					last++
					main = betaContent
					mainVersions[last] = main
				}
			}
		case 6:
			if len(rules) > 0 {
				s = r.rules(k, s, []rule{}, 0)
				rules = nil
				betaContent = ""
			}
		}
		require(s.Last == last && s.Global == last && s.Versions[last].Content == main, "model mismatch seed=%d step=%d operation=%d", r.seed, step, op)
		require((s.Beta != nil) == (len(rules) > 0), "model beta mismatch seed=%d step=%d", r.seed, step)
		for i, c := range clients {
			ruleID := ""
			if len(rules) > 0 {
				for _, rule := range rules {
					matchesFixture := (i == 1 || (i == 3 && rule.ID == "specific"))
					if rule.Enabled && matchesFixture {
						ruleID = rule.ID
						break
					}
				}
			}
			expected := main
			v := last
			if ruleID != "" {
				expected = betaContent
				v = 0
			}
			r.observed(observers[i], k, s, expected, ruleID, v)
			r.get(c, k, s, expected, ruleID, v)
		}
	}
}
