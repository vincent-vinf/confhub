package settings_test

import (
	"testing"
	"time"

	"gitlab.bodesitech.com/bodesi/confhub/internal/settings"
)

func environment(extra map[string]string) func(string) string {
	return func(key string) string {
		if value, ok := extra[key]; ok {
			return value
		}
		if key == "CONFHUB_DSN" {
			return "postgres://test/test"
		}
		if key == "CONFHUB_JWT_SECRET" {
			return "01234567890123456789012345678901"
		}
		return ""
	}
}

func TestDefaultsAndFlagPrecedence(t *testing.T) {
	s, err := settings.Parse(nil, environment(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.PollInterval != 100*time.Millisecond || s.HistoryLimit != 100 || s.CacheBytes != 64<<20 || s.CookieSecure || s.JWTExpiry != 2*time.Hour {
		t.Fatalf("unexpected defaults: %+v", s)
	}
	s, err = settings.Parse([]string{"--listen", ":9090", "--history-limit", "3", "--cookie-secure", "false", "--poll-interval", "20ms"}, environment(map[string]string{"CONFHUB_LISTEN": ":8081", "CONFHUB_HISTORY_LIMIT": "bad", "CONFHUB_COOKIE_SECURE": "true", "CONFHUB_POLL_INTERVAL": "50ms"}))
	if err != nil {
		t.Fatal(err)
	}
	if s.Listen != ":9090" || s.HistoryLimit != 3 || s.CookieSecure || s.PollInterval != 20*time.Millisecond {
		t.Fatalf("flags did not override environment: %+v", s)
	}
}

func TestInvalidEnvironmentAndArgumentsAreRejected(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"POLL_INTERVAL", "invalid"}, {"POLL_INTERVAL", "0"}, {"POLL_INTERVAL", "1us"}, {"POLL_INTERVAL", "2s"},
		{"SYNC_FAILURE_TIMEOUT", "1ms"}, {"JWT_EXPIRY", "-1s"}, {"CLEANUP_INTERVAL", "0s"}, {"EVENT_RETENTION", "oops"},
		{"CACHE_BYTES", "-1"}, {"CACHE_BYTES", "bad"}, {"HISTORY_LIMIT", "0"}, {"HISTORY_LIMIT", "bad"},
		{"COOKIE_SECURE", "maybe"}, {"DATABASE", "sqlite"}, {"JWT_SECRET", "short"},
	} {
		t.Run(tc.name+"/"+tc.value, func(t *testing.T) {
			if _, err := settings.Parse(nil, environment(map[string]string{"CONFHUB_" + tc.name: tc.value})); err == nil {
				t.Fatal("invalid environment accepted")
			}
		})
	}
	for _, args := range [][]string{{"--unknown"}, {"extra"}, {"--dsn", ""}} {
		if _, err := settings.Parse(args, environment(nil)); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestMigrationNeedsDSNButDoesNotRequireJWT(t *testing.T) {
	s, err := settings.Parse([]string{"migrate"}, func(key string) string {
		if key == "CONFHUB_DSN" {
			return "postgres://test/test"
		}
		return ""
	})
	if err != nil || !s.Migrate {
		t.Fatal(s, err)
	}
	if _, err := settings.Parse([]string{"migrate"}, func(string) string { return "" }); err == nil {
		t.Fatal("migration without DSN accepted")
	}
	if _, err := settings.Parse([]string{"--poll-interval", "1ms", "--sync-failure-timeout", "2ms", "--cache-bytes", "0"}, environment(nil)); err != nil {
		t.Fatal("legal minimum rejected", err)
	}
}
