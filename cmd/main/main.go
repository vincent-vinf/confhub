package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
	"github.com/vincent-vinf/confhub/internal/server"
	"github.com/vincent-vinf/confhub/internal/settings"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/syncer"
)

var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	options, err := settings.FromEnvironment(os.Args[1:])
	if err != nil {
		return err
	}
	if !options.ReadOnly {
		if err = storage.Migrate(options.Database, options.DSN, !options.Migrate); err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}
	if options.Migrate {
		slog.Info("schema migration completed")
		return nil
	}
	bootstrapCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	store, err := storage.Open(bootstrapCtx, options.Database, options.DSN)
	if err != nil {
		return err
	}
	defer store.Close()
	if options.ReadOnly {
		store.DisableWrites()
		if err = store.CheckSchema(bootstrapCtx); err != nil {
			return err
		}
	} else {
		if err = store.InitializeAdmin(bootstrapCtx, options.AdminPassword); err != nil {
			return err
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var source syncer.Source = store
	var worker *replication.Worker
	var publisher *replication.Publisher
	if options.Role == "cloud" {
		publisher = replication.NewPublisher(store, options.PollInterval)
	}
	if options.Role == "edge" {
		source = storage.MirrorSource{Store: store}
		dial := func(ctx context.Context) (replication.Conn, error) {
			if options.ReplicationTransport == "nats" {
				return transport.DialNATS(ctx, options.ReplicationURL, options.ReplicationSubject, options.ReplicationToken)
			}
			return transport.DialWebSocket(ctx, options.ReplicationURL, options.ReplicationToken)
		}
		worker = replication.NewWorker(store, dial, options.ReplicationLease)
	}
	hub := syncer.New(source, syncer.Options{PollInterval: options.PollInterval, FailureTimeout: options.SyncFailureTimeout, CacheBytes: options.CacheBytes})
	serverOptions := server.Options{JWTSecret: options.JWTSecret, JWTExpiry: options.JWTExpiry, CookieSecure: options.CookieSecure, StaticDir: options.StaticDir, ReadOnly: options.ReadOnly, Edge: options.Role == "edge", Context: ctx, ReplicationWorker: worker, ReplicationPublisher: publisher}
	if options.Role == "cloud" && options.ReplicationTransport == "websocket" {
		serverOptions.ReplicationToken = options.ReplicationToken
	}
	app, err := server.New(store, hub, serverOptions)
	if err != nil {
		return err
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); hub.Run(ctx) }()
	if !options.ReadOnly {
		workers.Add(2)
		go func() { defer workers.Done(); app.RunPresence(ctx) }()
		go func() { defer workers.Done(); maintain(ctx, store, options) }()
	}
	if publisher != nil {
		workers.Add(1)
		go func() { defer workers.Done(); publisher.Run(ctx) }()
	}
	if worker != nil {
		workers.Add(1)
		go func() { defer workers.Done(); worker.Run(ctx) }()
	}
	if options.Role == "cloud" && options.ReplicationTransport == "nats" {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for ctx.Err() == nil {
				_ = transport.ListenNATS(ctx, options.ReplicationURL, options.ReplicationSubject, options.ReplicationToken, func(sessionCtx context.Context, conn replication.Conn) {
					_ = publisher.Serve(sessionCtx, conn)
				})
				if ctx.Err() != nil {
					return
				}
				slog.Warn("replication broker unavailable; reconnecting")
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		}()
	}
	httpServer := &http.Server{Addr: options.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	result := make(chan error, 1)
	go func() { result <- httpServer.ListenAndServe() }()
	slog.Info("service started", "version", version, "listen", options.Listen, "database", options.Database, "role", options.Role, "read_only", options.ReadOnly)
	select {
	case <-ctx.Done():
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	workers.Wait()
	return errors.Join(err, shutdownErr)
}
func maintain(ctx context.Context, store *storage.Store, options settings.Settings) {
	owner := uuid.NewString()
	timer := time.NewTicker(options.CleanupInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			batchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := store.Cleanup(batchCtx, owner, options.HistoryLimit, options.EventRetention, 256)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("maintenance batch failed")
			}
		}
	}
}
