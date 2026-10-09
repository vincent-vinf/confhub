package config_test

import (
	"testing"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func TestGrayRulesUseFirstMatchAndFallThroughWhenDisabled(t *testing.T) {
	state := config.State{ID: "cfg", Revision: 7, GlobalVersion: 3, Versions: map[int64]config.Version{
		1: {Number: 1, Content: "old", Format: "text"}, 2: {Number: 2, Content: "canary", Format: "text"}, 3: {Number: 3, Content: "global", Format: "text"},
	}, Rules: []config.Rule{
		{ID: "specific", Enabled: true, TargetVersion: 1, Conditions: []config.Condition{{Tag: "region", Operator: "eq", Values: []string{"east"}}, {Tag: "sys.hostname", Operator: "in", Values: []string{"node-a", "node-b"}}}},
		{ID: "region", Enabled: true, TargetVersion: 2, Conditions: []config.Condition{{Tag: "region", Operator: "eq", Values: []string{"east"}}}},
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
		{ID: "r", TargetVersion: 1},
		{ID: "r", TargetVersion: 1, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"a", "b"}}}},
		{ID: "r", TargetVersion: 1, Conditions: []config.Condition{{Tag: "env", Operator: "regex", Values: []string{".*"}}}},
	} {
		if err := config.ValidateRules([]config.Rule{r}); err == nil {
			t.Fatalf("invalid rule accepted: %+v", r)
		}
	}
}
