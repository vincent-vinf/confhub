package clientinfo

import (
	"fmt"
	"strings"

	"github.com/vincent-vinf/confhub/internal/config"
)

// Filter combines connection fields with conditions on a single subscription.
// TagValue nil matches any value; a pointer to "" matches an empty tag value.
type Filter struct {
	Search    string
	Namespace string
	Group     string
	Name      string
	Status    string
	Tag       string
	TagValue  *string
}

func (f Filter) Validate() error {
	if len(f.Search) > 512 || len(f.Namespace) > 255 || len(f.Group) > 255 || len(f.Name) > 255 ||
		len(f.Tag) > 128 || (f.TagValue != nil && (f.Tag == "" || len(*f.TagValue) > 512)) {
		return fmt.Errorf("%w: invalid client filter", config.ErrInvalid)
	}
	switch f.Status {
	case "", "main", "beta", "deleted", "waiting", "unsubscribed":
	default:
		return fmt.Errorf("%w: invalid client status", config.ErrInvalid)
	}
	return nil
}

func (f Filter) Matches(c Client) bool {
	if f.Search != "" {
		found := false
		for _, value := range []string{c.ID, c.InstanceID, c.SourceAddress, c.Tags["sys.ip"], c.Tags["sys.hostname"]} {
			found = found || strings.Contains(strings.ToLower(value), strings.ToLower(f.Search))
		}
		if !found {
			return false
		}
	}
	if f.Tag != "" {
		value, exists := c.Tags[f.Tag]
		if !exists || (f.TagValue != nil && value != *f.TagValue) {
			return false
		}
	}
	if f.Status == "unsubscribed" {
		return len(c.Subscriptions) == 0 && f.Namespace == "" && f.Group == "" && f.Name == ""
	}
	if f.Namespace == "" && f.Group == "" && f.Name == "" && f.Status == "" {
		return true
	}
	for _, sub := range c.Subscriptions {
		if (f.Namespace != "" && sub.Key.Namespace != f.Namespace) ||
			(f.Group != "" && sub.Key.Group != f.Group) ||
			(f.Name != "" && !strings.Contains(strings.ToLower(sub.Key.Name), strings.ToLower(f.Name))) {
			continue
		}
		if f.Status == "" ||
			(f.Status == "waiting" && !sub.Sent) ||
			(f.Status == "deleted" && sub.Sent && sub.Deleted) ||
			(f.Status == "beta" && sub.Sent && !sub.Deleted && sub.Beta) ||
			(f.Status == "main" && sub.Sent && !sub.Deleted && !sub.Beta) {
			return true
		}
	}
	return false
}
