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
	if err = storage.Migrate(options.Database, options.DSN, !options.Migrate); err != nil {
		return fmt.Errorf("migration: %w", err)
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
	if err = store.InitializeAdmin(bootstrapCtx, options.AdminPassword); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	hub := syncer.New(store, syncer.Options{PollInterval: options.PollInterval, FailureTimeout: options.SyncFailureTimeout, CacheBytes: options.CacheBytes})
	app, err := server.New(store, hub, server.Options{JWTSecret: options.JWTSecret, JWTExpiry: options.JWTExpiry, CookieSecure: options.CookieSecure, StaticDir: options.StaticDir})
	if err != nil {
		return err
	}
	var workers sync.WaitGroup
	workers.Add(3)
	go func() { defer workers.Done(); hub.Run(ctx) }()
	go func() { defer workers.Done(); app.RunPresence(ctx) }()
	go func() { defer workers.Done(); maintain(ctx, store, options) }()
	httpServer := &http.Server{Addr: options.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	result := make(chan error, 1)
	go func() { result <- httpServer.ListenAndServe() }()
	slog.Info("service started", "version", version, "listen", options.Listen, "database", options.Database)
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
