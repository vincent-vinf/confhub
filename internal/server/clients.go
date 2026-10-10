package server

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
)

func (s *Server) clients(c *gin.Context) {
	limit := 25
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			s.respond(c, nil, fmt.Errorf("%w: invalid page limit", config.ErrInvalid))
			return
		}
		limit = v
	}
	page, err := s.store.Clients(c.Request.Context(), c.Query("after"), limit)
	s.respond(c, page, err)
}
func (s *Server) clientTags(c *gin.Context) {
	values, err := s.store.TagSuggestions(c.Request.Context(), c.Query("tag"), c.Query("prefix"))
	s.respond(c, values, err)
}

// RunPresence shares compact connection metadata across management replicas.
// Presence failure never blocks publication or WebSocket transport writes.
func (s *Server) RunPresence(ctx context.Context) {
	if s.hub == nil {
		return
	}
	instance := uuid.NewString()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.store.RemovePresence(cleanupCtx, instance); err != nil {
			slog.Warn("client presence shutdown cleanup failed")
		}
	}()
	cycles := 0
	for {
		batchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		err := s.store.SyncPresence(batchCtx, instance, s.hub.Clients(), 20*time.Second)
		if err == nil && cycles%12 == 0 {
			err = s.store.CleanupPresence(batchCtx)
		}
		cancel()
		cycles++
		if err != nil && ctx.Err() == nil {
			slog.Warn("client presence refresh failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
