package storage_test

import (
	"testing"

	"github.com/vincent-vinf/confhub/internal/storage"
)

func TestUserScopePermissions(t *testing.T) {
	user := storage.User{Enabled: true, Grants: []storage.Grant{{Namespace: "dev", Access: "write"}, {Namespace: "prod", Group: "payments", Access: "read"}}}
	for _, tc := range []struct {
		action, namespace, group string
		want                     bool
	}{
		{"config.write", "dev", "new-group", true}, {"config.read", "dev", "", true},
		{"config.read", "prod", "payments", true}, {"config.write", "prod", "payments", false},
		{"config.read", "prod", "other", false}, {"config.read", "prod", "", true},
		{"config.read", "other", "payments", false}, {"clients.read", "", "", false},
		{"users.manage", "", "", false}, {"organization.manage", "", "", false}, {"unknown", "dev", "", false},
	} {
		if got := user.Allows(tc.action, tc.namespace, tc.group); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
	user.Grants = append(user.Grants, storage.Grant{Namespace: "prod", Access: "write"})
	if !user.Allows("config.write", "prod", "payments") {
		t.Fatal("grants must union")
	}
	user.Grants = []storage.Grant{{Access: "read"}}
	if !user.Allows("config.read", "future", "future") || user.Allows("config.write", "future", "future") {
		t.Fatal("all namespaces read")
	}
	user.Grants = []storage.Grant{{Namespace: "*", Group: "*", Access: "write"}}
	if user.Allows("config.read", "dev", "a") {
		t.Fatal("literal * expanded")
	}
	user.ClientsRead = true
	if !user.Allows("clients.read", "", "") {
		t.Fatal("clients permission")
	}
	user.SystemAdmin = true
	if !user.Allows("users.manage", "", "") {
		t.Fatal("admin")
	}
	user.Enabled = false
	if user.Allows("users.manage", "", "") {
		t.Fatal("disabled admin")
	}
}
