package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gitlab.bodesitech.com/bodesi/confhub/internal/config"
	"gitlab.bodesitech.com/bodesi/confhub/internal/syncer"
)

var wsBuffers = &sync.Pool{}

func (s *Server) watch(c *gin.Context) {
	if s.hub == nil || !s.hub.Ready() {
		c.AbortWithStatus(503)
		return
	}
	tags, err := queryTags(c)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	session, err := s.hub.NewSession(tags)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	defer session.Close()
	upgrader := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024, WriteBufferPool: wsBuffers, CheckOrigin: func(r *http.Request) bool {
		raw := r.Header.Get("Origin")
		if raw == "" {
			return true
		}
		u, err := url.Parse(raw)
		scheme := "http"
		if r.TLS != nil || s.options.CookieSecure {
			scheme = "https"
		}
		return err == nil && u.Host == r.Host && u.Scheme == scheme && u.User == nil
	}}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	session.Connected(c.Request.RemoteAddr)
	slog.Info("client connected", "source_address", c.Request.RemoteAddr)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); defer conn.Close(); s.writeSnapshots(ctx, conn, session) }()
	conn.SetReadLimit(64 << 10)
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(30 * time.Second)) })
	for {
		var message struct {
			Op  string     `json:"op"`
			Key config.Key `json:"key"`
		}
		if err = conn.ReadJSON(&message); err != nil {
			break
		}
		opCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		switch message.Op {
		case "subscribe":
			err = session.Subscribe(opCtx, message.Key)
		case "unsubscribe":
			err = message.Key.Validate()
			if err == nil {
				session.Unsubscribe(message.Key)
			}
		default:
			err = config.ErrInvalid
		}
		stop()
		if err != nil {
			conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid subscription or unavailable service"), time.Now().Add(time.Second))
			break
		}
	}
	cancel()
	session.Close()
	conn.Close()
	<-writerDone
}
func (s *Server) writeSnapshots(ctx context.Context, conn *websocket.Conn, session *syncer.Session) {
	nextHeartbeat := time.Now().Add(10 * time.Second)
	for {
		if !time.Now().Before(nextHeartbeat) {
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
			nextHeartbeat = time.Now().Add(10 * time.Second)
		}
		deadlineCtx, cancel := context.WithDeadline(ctx, nextHeartbeat)
		value, err := session.Next(deadlineCtx)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			if err = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
			nextHeartbeat = time.Now().Add(10 * time.Second)
			continue
		}
		if err != nil {
			return
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err = conn.WriteJSON(value); err != nil {
			return
		}
		session.Sent(value)
	}
}
