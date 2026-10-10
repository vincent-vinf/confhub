package config_test

import (
	"errors"
	"strings"
	"testing"

	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
)

func TestNamesContentAndTagLimits(t *testing.T) {
	for _, name := range []string{"", " leading", "trailing ", "a/b", "a\\b", "a\x00b", "a\nb", "a\rb", "\xff", strings.Repeat("中", 43)} {
		if !errors.Is(config.ValidateName(name), config.ErrInvalid) {
			t.Fatalf("accepted name %q", name)
		}
	}
	for _, name := range []string{"中文配置.json", strings.Repeat("a", 128), "a b"} {
		if err := (config.Key{Namespace: name, Group: name, Name: name}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if (config.Key{Namespace: "public", Group: "DEFAULT_GROUP"}).Validate() == nil {
		t.Fatal("missing name accepted")
	}
	if err := config.ValidateContent("text", strings.Repeat("a", config.MaxContentBytes)); err != nil {
		t.Fatal(err)
	}
	if config.ValidateContent("text", "\xff") == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if err := config.ValidateContent("yaml", "---\na: 1\n---\nb: 2\n"); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", "text", "<a/><b/>", "<a>", "outside<a/>"} {
		if config.ValidateContent("xml", content) == nil {
			t.Fatalf("invalid XML accepted %q", content)
		}
	}
	tags := map[string]string{}
	for i := 0; i < 64; i++ {
		tags[strings.Repeat("x", i+1)] = ""
	}
	if err := config.ValidateTags(tags); err != nil {
		t.Fatal(err)
	}
	tags["extra"] = "x"
	if config.ValidateTags(tags) == nil {
		t.Fatal("65 tags accepted")
	}
	for _, tags := range []map[string]string{{"": "x"}, {strings.Repeat("x", 129): "x"}, {"x": strings.Repeat("x", 513)}} {
		if config.ValidateTags(tags) == nil {
			t.Fatal("oversize tag accepted")
		}
	}
}

func TestLiteralTagsAndMappedIPRanges(t *testing.T) {
	s := &config.State{ID: "id", GlobalVersion: 1, Versions: map[int64]config.Version{1: {Content: "global", Format: "text"}}, Beta: &config.Beta{Content: "beta", Format: "text"}}
	s.Rules = []config.Rule{{ID: "r", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{""}}, {Tag: "zone", Operator: "in", Values: []string{"east ", "west"}}}}}
	for _, tc := range []struct {
		tags  map[string]string
		match bool
	}{
		{map[string]string{"env": "", "zone": "east "}, true}, {map[string]string{"zone": "east "}, false}, {map[string]string{"env": "", "zone": "east"}, false}, {map[string]string{"env": "", "zone": "WEST"}, false},
	} {
		if got := config.Resolve(s, tc.tags); got.Beta != tc.match {
			t.Fatal(tc, got)
		}
	}
	s.Rules[0].Conditions = []config.Condition{{Tag: "ip", Operator: "ip_range", Values: []string{"::ffff:192.168.2.1", "192.168.2.5"}}}
	if err := config.ValidateRules(s.Rules); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.168.2.1", "::ffff:192.168.2.5"} {
		if !config.Resolve(s, map[string]string{"ip": ip}).Beta {
			t.Fatal(ip)
		}
	}
	for _, ip := range []string{"::1", "192.168.2.6", "fe80::1%eth0"} {
		if config.Resolve(s, map[string]string{"ip": ip}).Beta {
			t.Fatal(ip)
		}
	}
	s.Beta = nil
	if config.Resolve(s, map[string]string{"ip": "192.168.2.3"}).Beta {
		t.Fatal("missing beta selected")
	}
}

func TestRuleLimitsAndInvalidIdentities(t *testing.T) {
	valid := config.Rule{ID: "r", Enabled: true, Conditions: []config.Condition{{Tag: "env", Operator: "eq", Values: []string{"gray"}}}}
	for _, mutate := range []func(*config.Rule){
		func(r *config.Rule) { r.ID = "" }, func(r *config.Rule) { r.ID = strings.Repeat("x", 37) }, func(r *config.Rule) { r.Name = strings.Repeat("x", 129) },
		func(r *config.Rule) { r.Conditions = nil }, func(r *config.Rule) { r.Conditions = make([]config.Condition, 33) },
		func(r *config.Rule) {
			r.Conditions = []config.Condition{{Tag: "", Operator: "eq", Values: []string{"x"}}}
		},
		func(r *config.Rule) {
			r.Conditions = []config.Condition{{Tag: "env", Operator: "in", Values: []string{strings.Repeat("x", 513)}}}
		},
		func(r *config.Rule) {
			r.Conditions = []config.Condition{{Tag: "env", Operator: "in", Values: make([]string, 101)}}
		},
	} {
		r := valid
		mutate(&r)
		if config.ValidateRules([]config.Rule{r}) == nil {
			t.Fatal("invalid rule accepted", r)
		}
	}
	if config.ValidateRules([]config.Rule{valid, valid}) == nil {
		t.Fatal("duplicate rule IDs accepted")
	}
	if config.ValidateRules(make([]config.Rule, 101)) == nil {
		t.Fatal("101 rules accepted")
	}
}

func FuzzIPSingletonRange(f *testing.F) {
	for _, ip := range []string{"192.168.2.1", "::1", "::ffff:192.168.2.1", "bad", "fe80::1%eth0", ""} {
		f.Add(ip)
	}
	f.Fuzz(func(t *testing.T, ip string) {
		if len(ip) > 512 {
			return
		}
		rules := []config.Rule{{ID: "r", Enabled: true, Conditions: []config.Condition{{Tag: "ip", Operator: "ip_range", Values: []string{ip, ip}}}}}
		if config.ValidateRules(rules) != nil {
			return
		}
		s := &config.State{GlobalVersion: 1, Versions: map[int64]config.Version{1: {Content: "global"}}, Beta: &config.Beta{Content: "beta"}, Rules: rules}
		if got := config.Resolve(s, map[string]string{"ip": ip}); !got.Beta || got.Content != "beta" {
			t.Fatal("valid singleton excluded its own address", got)
		}
	})
}

func FuzzNamesAndTextValidation(f *testing.F) {
	for _, value := range []string{"中文.json", "a/b", "", "a\x00b", "\xff"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > config.MaxContentBytes+1 {
			return
		}
		_ = config.ValidateName(value)
		_ = config.ValidateContent("text", value)
		_ = config.ValidateTags(map[string]string{"value": value})
	})
}
