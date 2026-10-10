package config

import (
	"fmt"
	"net/netip"
)

// Resolve evaluates the same ordered rules for client reads and admin simulation.
func Resolve(state *State, tags map[string]string) Effective {
	target := state.GlobalVersion
	v := state.Versions[target]
	ruleID := ""
	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		match := true
		for _, condition := range rule.Conditions {
			value, exists := tags[condition.Tag]
			found := false
			if exists && condition.Operator == "ip_range" {
				start, end, valid := ipRange(condition.Values)
				ip, err := netip.ParseAddr(value)
				ip = ip.Unmap()
				found = valid && err == nil && ip.Zone() == "" && ip.BitLen() == start.BitLen() && ip.Compare(start) >= 0 && ip.Compare(end) <= 0
			} else if exists {
				for _, candidate := range condition.Values {
					if value == candidate {
						found = true
						break
					}
				}
			}
			if !found {
				match = false
				break
			}
		}
		if match {
			target = rule.Beta.BaseVersion
			v = Version{Content: rule.Beta.Content, Format: rule.Beta.Format}
			ruleID = rule.ID
			break
		}
	}
	return Effective{Sequence: state.Sequence, ID: state.ID, Key: state.Key, Revision: state.Revision, Version: target, Content: v.Content, Format: v.Format, RuleID: ruleID}
}
func ValidateRules(rules []Rule) error {
	if len(rules) > 100 {
		return fmt.Errorf("%w: at most 100 gray rules", ErrInvalid)
	}
	ids := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || len(r.ID) > 36 || ids[r.ID] || len(r.Conditions) == 0 || len(r.Conditions) > 32 {
			return fmt.Errorf("%w: invalid rule identity or conditions", ErrInvalid)
		}
		ids[r.ID] = true
		if len(r.Name) > 128 {
			return fmt.Errorf("%w: rule name too long", ErrInvalid)
		}
		for _, c := range r.Conditions {
			if c.Tag == "" || len(c.Tag) > 128 || len(c.Values) == 0 || len(c.Values) > 100 || (c.Operator != "eq" && c.Operator != "in" && c.Operator != "ip_range") || (c.Operator == "eq" && len(c.Values) != 1) {
				return fmt.Errorf("%w: invalid tag condition", ErrInvalid)
			}
			if c.Operator == "ip_range" {
				if _, _, ok := ipRange(c.Values); !ok {
					return fmt.Errorf("%w: IP range requires two ordered addresses of the same family", ErrInvalid)
				}
			}
			for _, value := range c.Values {
				if len(value) > 512 {
					return fmt.Errorf("%w: tag value too long", ErrInvalid)
				}
			}
		}
	}
	return nil
}
func ValidateTags(tags map[string]string) error {
	if len(tags) > 64 {
		return fmt.Errorf("%w: at most 64 tags", ErrInvalid)
	}
	for key, value := range tags {
		if key == "" || len(key) > 128 || len(value) > 512 {
			return fmt.Errorf("%w: invalid tag", ErrInvalid)
		}
	}
	return nil
}

// IP ranges are inclusive, numeric, and contain unscoped addresses of one family.
func ipRange(values []string) (netip.Addr, netip.Addr, bool) {
	if len(values) != 2 {
		return netip.Addr{}, netip.Addr{}, false
	}
	start, a := netip.ParseAddr(values[0])
	end, b := netip.ParseAddr(values[1])
	start = start.Unmap()
	end = end.Unmap()
	return start, end, a == nil && b == nil && start.Zone() == "" && end.Zone() == "" && start.BitLen() == end.BitLen() && start.Compare(end) <= 0
}
