package clientinfo_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/vincent-vinf/confhub/internal/clientinfo"
	"github.com/vincent-vinf/confhub/internal/config"
)

func TestClientFilters(t *testing.T) {
	str := func(s string) *string { return &s }
	c := clientinfo.Client{ID: "connection-abc", InstanceID: "replica-east", SourceAddress: "[::1]:1234",
		Tags: map[string]string{"sys.ip": "192.168.2.3", "sys.hostname": "Node-East", "env": "Gray", "empty": "", "literal": "%_ "},
		Subscriptions: []clientinfo.Subscription{
			{Key: config.Key{Namespace: "public", Group: "prod", Name: "App.yaml"}, Sent: true, Beta: true},
			{Key: config.Key{Namespace: "private", Group: "test", Name: "other.json"}, Sent: true},
			{Key: config.Key{Namespace: "public", Group: "test", Name: "removed"}, Sent: true, Deleted: true, Beta: true},
			{Key: config.Key{Namespace: "public", Group: "test", Name: "pending"}},
		}}
	for _, tc := range []struct {
		name string
		f    clientinfo.Filter
		want bool
	}{
		{"all", clientinfo.Filter{}, true},
		{"hostname case insensitive", clientinfo.Filter{Search: "node-east"}, true},
		{"ip", clientinfo.Filter{Search: "192.168.2."}, true},
		{"source", clientinfo.Filter{Search: "::1"}, true},
		{"connection", clientinfo.Filter{Search: "abc"}, true},
		{"instance", clientinfo.Filter{Search: "replica-east"}, true},
		{"no keyword", clientinfo.Filter{Search: "unknown"}, false},
		{"same subscription", clientinfo.Filter{Namespace: "public", Group: "prod", Name: "app", Status: "beta"}, true},
		{"no cross subscription matching", clientinfo.Filter{Namespace: "private", Group: "prod"}, false},
		{"no cross subscription state", clientinfo.Filter{Name: "App", Status: "main"}, false},
		{"exact namespace", clientinfo.Filter{Namespace: "pub"}, false},
		{"main", clientinfo.Filter{Status: "main"}, true},
		{"deleted", clientinfo.Filter{Status: "deleted"}, true},
		{"deleted is not beta", clientinfo.Filter{Name: "removed", Status: "beta"}, false},
		{"waiting", clientinfo.Filter{Name: "pending", Status: "waiting"}, true},
		{"waiting is not main", clientinfo.Filter{Name: "pending", Status: "main"}, false},
		{"unsubscribed", clientinfo.Filter{Status: "unsubscribed"}, false},
		{"tag exists", clientinfo.Filter{Tag: "env"}, true},
		{"tag exact", clientinfo.Filter{Tag: "env", TagValue: str("Gray")}, true},
		{"tag case sensitive", clientinfo.Filter{Tag: "env", TagValue: str("gray")}, false},
		{"tag literal", clientinfo.Filter{Tag: "literal", TagValue: str("%_ ")}, true},
		{"tag trailing spaces", clientinfo.Filter{Tag: "literal", TagValue: str("%_")}, false},
		{"tag empty", clientinfo.Filter{Tag: "empty", TagValue: str("")}, true},
		{"tag missing is not empty", clientinfo.Filter{Tag: "missing", TagValue: str("")}, false},
		{"combined", clientinfo.Filter{Search: "node", Tag: "env", TagValue: str("Gray"), Status: "beta"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Matches(c); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	empty := clientinfo.Client{}
	if !(clientinfo.Filter{Status: "unsubscribed"}).Matches(empty) || (clientinfo.Filter{Namespace: "public", Status: "unsubscribed"}).Matches(empty) {
		t.Fatal("unsubscribed semantics")
	}
}

func TestClientFilterValidation(t *testing.T) {
	long := strings.Repeat("x", 513)
	empty := ""
	for _, f := range []clientinfo.Filter{
		{Search: long}, {Namespace: long}, {Group: long}, {Name: long}, {Status: "unknown"}, {Tag: long}, {TagValue: &empty}, {Tag: "env", TagValue: &long},
	} {
		if !errors.Is(f.Validate(), config.ErrInvalid) {
			t.Fatalf("accepted invalid filter: %+v", f)
		}
	}
	for _, f := range []clientinfo.Filter{{}, {Status: "unsubscribed"}, {Tag: "env", TagValue: &empty}} {
		if err := f.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
