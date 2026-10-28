package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/vincent-vinf/confhub/internal/replication"
	"github.com/vincent-vinf/confhub/internal/replication/transport"
)

func (s *Server) replicate(c *gin.Context) {
	provided := sha256.Sum256([]byte(c.GetHeader("Authorization")))
	expected := sha256.Sum256([]byte("Bearer " + s.options.ReplicationToken))
	if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		c.AbortWithStatus(401)
		return
	}
	// Machine sessions never use browser cookies or cross-origin browser access.
	if c.GetHeader("Origin") != "" {
		c.AbortWithStatus(403)
		return
	}
	select {
	case s.replicationSlots <- struct{}{}:
		defer func() { <-s.replicationSlots }()
	default:
		c.AbortWithStatus(503)
		return
	}
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, WriteBufferPool: wsBuffers}
	raw, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	conn := transport.WebSocket(raw)
	defer conn.Close()
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	if s.options.Context != nil {
		stop := context.AfterFunc(s.options.Context, cancel)
		defer stop()
	}
	if s.options.ReplicationPublisher != nil {
		_ = s.options.ReplicationPublisher.Serve(ctx, conn)
	}
}
func (s *Server) edgeStatus(c *gin.Context) {
	status, err := s.store.MirrorStatus(c.Request.Context())
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	connection := replication.ConnectionStatus{}
	if s.options.ReplicationWorker != nil {
		connection = s.options.ReplicationWorker.Status()
	}
	c.JSON(200, gin.H{"mirror": status, "connection": connection})
}
