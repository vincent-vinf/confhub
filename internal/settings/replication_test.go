package settings_test

import (
	"strings"
	"testing"

	"github.com/vincent-vinf/confhub/internal/settings"
)

func TestEdgeUsesLocalLoginAndDedicatedReplicationCredentials(t *testing.T) {
	env := func(key string) string {
		switch key {
		case "CONFHUB_DSN":
			return "postgres://test/local"
		case "CONFHUB_ROLE":
			return "edge"
		case "CONFHUB_REPLICATION_URL":
			return "ws://cloud:8080/api/replication/watch"
		case "CONFHUB_REPLICATION_TOKEN":
			return "01234567890123456789012345678901"
		case "CONFHUB_JWT_SECRET":
			return "edge-only-jwt-secret-at-least-32-bytes"
		}
		return ""
	}
	s, err := settings.Parse(nil, env)
	if err != nil || s.Role != "edge" || s.ReplicationTransport != "websocket" {
		t.Fatal(s, err)
	}
	for _, args := range [][]string{{"--jwt-secret", ""}, {"--jwt-secret", "short"}, {"--role", "unknown"}, {"--replication-token", "short"}, {"--replication-url", "http://cloud"}, {"--replication-transport", "unknown"}, {"--replication-lease", "0s"}, {"--replication-transport", "nats", "--replication-url", "nats://broker:4222", "--replication-subject", "confhub.*"}} {
		if _, err := settings.Parse(args, env); err == nil {
			t.Fatal("accepted invalid edge settings", args)
		}
	}
}

func TestReadOnlyModeIsIndependentFromCloudEdgeRoles(t *testing.T) {
	env := func(key string) string {
		return map[string]string{
			"CONFHUB_DSN":               "postgres://test/local",
			"CONFHUB_JWT_SECRET":        "test-jwt-secret-at-least-32-bytes-long",
			"CONFHUB_REPLICATION_TOKEN": "test-replication-token-at-least-32-bytes",
			"CONFHUB_REPLICATION_URL":   "ws://cloud:8080/api/replication/watch",
		}[key]
	}
	for _, role := range []string{"standalone", "cloud", "edge"} {
		t.Run(role, func(t *testing.T) {
			if _, err := settings.Parse([]string{"--role", role}, env); err != nil {
				t.Fatal(err)
			}
			s, err := settings.Parse([]string{"--role", role, "--read-only"}, env)
			if role == "standalone" {
				if err != nil || !s.ReadOnly {
					t.Fatal(s, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "read-only requires standalone") {
				t.Fatalf("accepted incompatible role %s: %v", role, err)
			}
		})
	}
}
