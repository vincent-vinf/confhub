// Package server exposes the management and client HTTP/WebSocket protocols.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
	"github.com/vincent-vinf/confhub/internal/syncer"
)

type Options struct {
	JWTSecret    string
	JWTExpiry    time.Duration
	CookieSecure bool
	StaticDir    string
}
type Server struct {
	hub     *syncer.Hub
	store   *storage.Store
	options Options
	router  *gin.Engine
}

func New(store *storage.Store, hub *syncer.Hub, options Options) (*Server, error) {
	if len(options.JWTSecret) < 32 || options.JWTExpiry <= 0 {
		return nil, errors.New("JWT secret must contain at least 32 bytes and expiry must be positive")
	}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.SetTrustedProxies(nil)
	s := &Server{store: store, hub: hub, options: options, router: r}
	r.Use(func(c *gin.Context) { c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<20); c.Next() })
	r.POST("/api/admin/login", s.sameOrigin, s.login)
	a := r.Group("/api/admin", s.authenticate, s.sameOrigin)
	a.POST("/logout", s.logout)
	a.POST("/password", s.password)
	a.GET("/namespaces", func(c *gin.Context) { v, err := s.store.Namespaces(c.Request.Context()); s.respond(c, v, err) })
	s.configRoutes(a)
	a.GET("/clients", s.clients)
	a.GET("/client-tags", s.clientTags)
	r.GET("/api/client/config", s.clientGet)
	r.GET("/api/client/watch", s.watch)
	r.GET("/health/live", func(c *gin.Context) { c.Status(200) })
	r.GET("/health/ready", func(c *gin.Context) {
		if s.hub == nil || !s.hub.Ready() {
			c.Status(503)
			return
		}
		c.Status(200)
	})
	r.NoRoute(s.static)
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.router }
func (s *Server) respond(c *gin.Context, value any, err error) {
	if err == nil {
		c.JSON(200, value)
		return
	}
	status := 500
	message := "internal server error"
	switch {
	case errors.Is(err, config.ErrNotFound):
		status = 404
		message = err.Error()
	case errors.Is(err, config.ErrConflict):
		status = 409
		message = err.Error()
	case errors.Is(err, config.ErrInvalid), errors.Is(err, config.ErrNotEmpty):
		status = 400
		message = err.Error()
	case errors.Is(err, syncer.ErrUnavailable):
		status = 503
		message = err.Error()
	case errors.Is(err, storage.ErrUnauthorized):
		status = 401
		message = "invalid credentials"
	}
	if status == 500 {
		slog.Error("request failed", "path", c.Request.URL.Path)
	}
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}
func bind(c *gin.Context, value any) bool {
	if err := c.ShouldBindJSON(value); err != nil {
		c.AbortWithStatusJSON(400, gin.H{"error": "invalid request body"})
		return false
	}
	return true
}

// Strict management payloads reject retired fields instead of publishing to an
// unintended target. Keep decode errors specific rather than claiming beta edits.
func bindStrict(c *gin.Context, value any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		c.AbortWithStatusJSON(400, gin.H{"error": "invalid request body: " + err.Error()})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		c.AbortWithStatusJSON(400, gin.H{"error": "invalid request body: expected one JSON object"})
		return false
	}
	return true
}
