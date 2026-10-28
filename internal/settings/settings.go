// Package settings reads service settings exclusively from flags and environment.
package settings

import (
	"flag"
	"fmt"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
	"io"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Settings struct {
	Listen, Database, DSN, AdminPassword, JWTSecret, StaticDir                       string
	PollInterval, SyncFailureTimeout, JWTExpiry, CleanupInterval, EventRetention     time.Duration
	CacheBytes                                                                       int64
	HistoryLimit                                                                     int
	CookieSecure                                                                     bool
	Migrate                                                                          bool
	Role, ReplicationTransport, ReplicationURL, ReplicationToken, ReplicationSubject string
	ReplicationLease                                                                 time.Duration
	ReadOnly                                                                         bool
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
	readOnly, readOnlyErr := strconv.ParseBool(env("READ_ONLY", "false"))
	f.BoolVar(&s.ReadOnly, "read-only", readOnly, "serve existing data without database writes")
	f.StringVar(&s.Role, "role", env("ROLE", "standalone"), "standalone, cloud or edge")
	f.StringVar(&s.ReplicationTransport, "replication-transport", env("REPLICATION_TRANSPORT", "websocket"), "websocket or nats")
	f.StringVar(&s.ReplicationURL, "replication-url", env("REPLICATION_URL", ""), "cloud WebSocket endpoint or NATS broker URL")
	f.StringVar(&s.ReplicationToken, "replication-token", env("REPLICATION_TOKEN", ""), "dedicated cloud-edge token (at least 32 bytes)")
	f.StringVar(&s.ReplicationSubject, "replication-subject", env("REPLICATION_SUBJECT", "confhub.replication"), "NATS replication subject prefix")
	// Register string defaults so invalid environment values are rejected rather
	// than silently replaced by defaults.
	values := map[string]*string{}
	for _, v := range []struct{ name, key, def string }{
		{"poll-interval", "POLL_INTERVAL", "100ms"}, {"sync-failure-timeout", "SYNC_FAILURE_TIMEOUT", "2s"},
		{"jwt-expiry", "JWT_EXPIRY", "2h"}, {"cleanup-interval", "CLEANUP_INTERVAL", "1m"}, {"event-retention", "EVENT_RETENTION", "24h"},
		{"cache-bytes", "CACHE_BYTES", "67108864"}, {"history-limit", "HISTORY_LIMIT", "100"}, {"cookie-secure", "COOKIE_SECURE", "false"},
		{"replication-lease", "REPLICATION_LEASE", "30s"},
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
	// An explicit flag overrides the environment, including an invalid value.
	f.Visit(func(v *flag.Flag) {
		if v.Name == "read-only" {
			readOnlyErr = nil
		}
	})
	if readOnlyErr != nil {
		return s, fmt.Errorf("read-only must be boolean")
	}
	if s.ReadOnly && s.Migrate {
		return s, fmt.Errorf("migrate is unavailable in read-only mode; migrate the writable database")
	}
	if f.NArg() != 0 {
		return s, fmt.Errorf("unexpected argument %q", f.Arg(0))
	}
	for name, p := range map[string]*time.Duration{"poll-interval": &s.PollInterval, "sync-failure-timeout": &s.SyncFailureTimeout, "jwt-expiry": &s.JWTExpiry, "cleanup-interval": &s.CleanupInterval, "event-retention": &s.EventRetention, "replication-lease": &s.ReplicationLease} {
		v, err := time.ParseDuration(*values[name])
		if err != nil || v <= 0 {
			return s, fmt.Errorf("%s must be a positive duration", name)
		}
		*p = v
	}
	if s.PollInterval < time.Millisecond || s.SyncFailureTimeout < 2*time.Millisecond {
		return s, fmt.Errorf("poll-interval must be at least 1ms and sync-failure-timeout at least 2ms")
	}
	if s.PollInterval > s.SyncFailureTimeout/2 {
		return s, fmt.Errorf("poll-interval must not exceed half of sync-failure-timeout")
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
	if s.Role != "standalone" && s.Role != "cloud" && s.Role != "edge" {
		return s, fmt.Errorf("role must be standalone, cloud or edge")
	}
	if s.ReadOnly && s.Role != "standalone" {
		return s, fmt.Errorf("read-only requires standalone role; edge already rejects management writes and must persist replication data")
	}
	if s.ReplicationTransport != "websocket" && s.ReplicationTransport != "nats" {
		return s, fmt.Errorf("replication-transport must be websocket or nats")
	}
	if s.ReplicationLease < 3*time.Second {
		return s, fmt.Errorf("replication-lease must be at least 3s")
	}
	if !s.Migrate && s.Role != "standalone" {
		if len(s.ReplicationToken) < 32 {
			return s, fmt.Errorf("replication-token must contain at least 32 bytes")
		}
		if s.Role == "edge" || s.ReplicationTransport == "nats" {
			u, err := url.Parse(s.ReplicationURL)
			if err != nil || u.Host == "" || u.Fragment != "" || u.RawQuery != "" {
				return s, fmt.Errorf("invalid replication-url")
			}
			if s.ReplicationTransport == "websocket" && (u.Scheme != "ws" && u.Scheme != "wss" || u.User != nil) {
				return s, fmt.Errorf("websocket replication-url must use ws or wss without user info")
			}
			if s.ReplicationTransport == "nats" && (u.Scheme != "nats" && u.Scheme != "tls" || !transport.ValidSubject(s.ReplicationSubject)) {
				return s, fmt.Errorf("invalid NATS replication URL or subject")
			}
		}
	}
	return s, nil
}
func FromEnvironment(args []string) (Settings, error) { return Parse(args, os.Getenv) }
