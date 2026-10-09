// Package settings reads service settings exclusively from flags and environment.
package settings

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

type Settings struct {
	Listen, Database, DSN, AdminPassword, JWTSecret, StaticDir                   string
	PollInterval, SyncFailureTimeout, JWTExpiry, CleanupInterval, EventRetention time.Duration
	CacheBytes                                                                   int64
	HistoryLimit                                                                 int
	CookieSecure                                                                 bool
	Migrate                                                                      bool
}

func Parse(args []string, getenv func(string) string) (Settings, error) {
	s := Settings{}
	f := flag.NewFlagSet("confhub", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	env := func(name, fallback string) string {
		if v := getenv("CONFHUB_" + name); v != "" {
			return v
		}
		return fallback
	}
	f.StringVar(&s.Listen, "listen", env("LISTEN", ":8080"), "HTTP listen address")
	f.StringVar(&s.Database, "database", env("DATABASE", "postgres"), "postgres or mysql")
	f.StringVar(&s.DSN, "dsn", env("DSN", ""), "database DSN")
	f.StringVar(&s.AdminPassword, "admin-password", env("ADMIN_PASSWORD", ""), "initial admin password")
	f.StringVar(&s.JWTSecret, "jwt-secret", env("JWT_SECRET", ""), "shared JWT signing secret (at least 32 bytes)")
	f.StringVar(&s.StaticDir, "static-dir", env("STATIC_DIR", "/app/frontend"), "React build directory")
	// Register string defaults so invalid environment values are rejected rather
	// than silently replaced by defaults.
	values := map[string]*string{}
	for _, v := range []struct{ name, key, def string }{
		{"poll-interval", "POLL_INTERVAL", "100ms"}, {"sync-failure-timeout", "SYNC_FAILURE_TIMEOUT", "2s"},
		{"jwt-expiry", "JWT_EXPIRY", "2h"}, {"cleanup-interval", "CLEANUP_INTERVAL", "1m"}, {"event-retention", "EVENT_RETENTION", "24h"},
		{"cache-bytes", "CACHE_BYTES", "67108864"}, {"history-limit", "HISTORY_LIMIT", "100"}, {"cookie-secure", "COOKIE_SECURE", "false"},
	} {
		p := new(string)
		f.StringVar(p, v.name, env(v.key, v.def), v.key)
		values[v.name] = p
	}
	if len(args) > 0 && args[0] == "migrate" {
		s.Migrate = true
		args = args[1:]
	}
	if err := f.Parse(args); err != nil {
		return s, err
	}
	if f.NArg() != 0 {
		return s, fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	for name, p := range map[string]*time.Duration{"poll-interval": &s.PollInterval, "sync-failure-timeout": &s.SyncFailureTimeout, "jwt-expiry": &s.JWTExpiry, "cleanup-interval": &s.CleanupInterval, "event-retention": &s.EventRetention} {
		v, err := time.ParseDuration(*values[name])
		if err != nil || v <= 0 {
			return s, fmt.Errorf("%s must be a positive duration", name)
		}
		*p = v
	}
	var err error
	s.CacheBytes, err = strconv.ParseInt(*values["cache-bytes"], 10, 64)
	if err != nil || s.CacheBytes < 0 {
		return s, fmt.Errorf("cache-bytes must be nonnegative")
	}
	s.HistoryLimit, err = strconv.Atoi(*values["history-limit"])
	if err != nil || s.HistoryLimit < 1 {
		return s, fmt.Errorf("history-limit must be positive")
	}
	s.CookieSecure, err = strconv.ParseBool(*values["cookie-secure"])
	if err != nil {
		return s, fmt.Errorf("cookie-secure must be boolean")
	}
	if s.Database != "postgres" && s.Database != "mysql" {
		return s, fmt.Errorf("database must be postgres or mysql")
	}
	if s.DSN == "" {
		return s, fmt.Errorf("dsn is required")
	}
	if !s.Migrate && len(s.JWTSecret) < 32 {
		return s, fmt.Errorf("jwt-secret must contain at least 32 bytes")
	}
	return s, nil
}
func FromEnvironment(args []string) (Settings, error) { return Parse(args, os.Getenv) }
