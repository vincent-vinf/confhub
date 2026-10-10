package config_test

import (
	"testing"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func TestGrayRulesUseFirstMatchAndFallThroughWhenDisabled(t *testing.T) {
	state := config.State{ID: "cfg", Revision: 7, GlobalVersion: 3, Versions: map[int64]config.Version{
		1: {Number: 1, Content: "old", Format: "text"}, 2: {Number: 2, Content: "canary", Format: "text"}, 3: {Number: 3, Content: "global", Format: "text"},
	}, Rules: []config.Rule{
		{ID: "specific", Enabled: true, Beta: config.Beta{BaseVersion: 1, Content: "old", Format: "text"}, Conditions: []config.Condition{{Tag: "region", Operator: "eq", Values: []string{"east"}}, {Tag: "sys.hostname", Operator: "in", Values: []string{"node-a", "node-b"}}}},
		{ID: "region", Enabled: true, Beta: config.Beta{BaseVersion: 2, Content: "canary", Format: "text"}, Conditions: []config.Condition{{Tag: "region", Operator: "eq", Values: []string{"east"}}}},
	}}
	got := config.Resolve(&state, map[string]string{"region": "east", "sys.hostname": "node-a"})
	if got.Version != 1 || got.RuleID != "specific" {
		t.Fatalf("first matching rule: %+v", got)
	}
	state.Rules[0].Enabled = false
	got = config.Resolve(&state, map[string]string{"region": "east", "sys.hostname": "node-a"})
	if got.Version != 2 || got.RuleID != "region" {
		t.Fatalf("disabled rule must fall through: %+v", got)
	}
	got = config.Resolve(&state, map[string]string{"sys.hostname": "node-a"})
	if got.Version != 3 || got.RuleID != "" {
		t.Fatalf("missing AND tag must fall back: %+v", got)
	}
}

func TestGrayRuleValidationRejectsAmbiguousConditions(t *testing.T) {
	for _, r := range []config.Rule{
		{ID: "r"},
		{ID: "r", Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"a", "b"}}}},
		{ID: "r", Conditions: []config.Condition{{Tag: "env", Operator: "regex", Values: []string{".*"}}}},
	} {
		if err := config.ValidateRules([]config.Rule{r}); err == nil {
			t.Fatalf("invalid rule accepted: %+v", r)
		}
	}
}

func TestIPRangeIsInclusiveAndUsesAddressOrdering(t *testing.T) {
	state := config.State{GlobalVersion: 1, Versions: map[int64]config.Version{1: {Content: "global"}}, Rules: []config.Rule{{ID: "ip", Enabled: true, Beta: config.Beta{BaseVersion: 1, Content: "beta"}, Conditions: []config.Condition{{Tag: "sys.ip", Operator: "ip_range", Values: []string{"192.168.2.1", "192.168.2.5"}}}}}}
	if err := config.ValidateRules(state.Rules); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ip    string
		match bool
	}{{"192.168.2.1", true}, {"192.168.2.3", true}, {"192.168.2.5", true}, {"192.168.2.0", false}, {"192.168.2.10", false}, {"host", false}, {"", false}} {
		got := config.Resolve(&state, map[string]string{"sys.ip": tc.ip})
		if (got.RuleID == "ip") != tc.match {
			t.Errorf("%q: %+v", tc.ip, got)
		}
	}
	for _, values := range [][]string{{"192.168.2.5", "192.168.2.1"}, {"192.168.2.1"}, {"bad", "192.168.2.5"}, {"192.168.2.1", "::1"}, {"fe80::1%eth0", "fe80::2%eth0"}} {
		state.Rules[0].Conditions[0].Values = values
		if config.ValidateRules(state.Rules) == nil {
			t.Errorf("invalid range accepted: %v", values)
		}
	}
	state.Rules[0].Conditions[0].Values = []string{"2001:db8::1", "2001:db8::5"}
	if err := config.ValidateRules(state.Rules); err != nil {
		t.Fatal(err)
	}
	if got := config.Resolve(&state, map[string]string{"sys.ip": "2001:db8::3"}); got.RuleID != "ip" {
		t.Fatal(got)
	}
}
