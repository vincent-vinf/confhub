package server

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAuditTextRemovesControlsAndTruncatesAtUTF8Boundary(t *testing.T) {
	for _, tc := range []struct {
		value string
		limit int
		want  string
	}{
		{"用户\n\t\x00名称", 64, "用户名称"},
		{"a灰度", 5, "a灰"},
		{"灰度", 2, ""},
		{strings.Repeat("x", 600), 512, strings.Repeat("x", 512)},
	} {
		got := auditText(tc.value, tc.limit)
		if got != tc.want || !utf8.ValidString(got) || len(got) > tc.limit {
			t.Fatalf("auditText: %q", got)
		}
	}
}

func TestAuditExcludesClientAndBackgroundQueries(t *testing.T) {
	for _, path := range []string{"/api/client/config", "/api/client/watch", "/api/admin/namespaces", "/api/admin/namespaces/:namespace/groups", "/api/admin/namespaces/:namespace/groups/:group/configs", "/api/admin/namespaces/:namespace/groups/:group/configs/:name/versions", "/api/admin/me", "/api/admin/users", "/api/admin/clients", "/api/admin/client-tags", "/api/admin/audits", "/health/live"} {
		if got := auditAction("GET", path); got != "" {
			t.Fatalf("background query %s audited as %s", path, got)
		}
	}
	if auditAction("POST", "/api/admin/namespaces/:namespace/groups/:group/configs/:name/simulate") != "config.simulate" {
		t.Fatal("simulation should remain audited as a read operation")
	}
}
