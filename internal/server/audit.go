package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
)

// Explicitly exclude background list queries and SDK traffic. Never decode or
// copy request/response bodies here: handlers attach whitelisted metadata only.
func auditAction(method, path string) string {
	base := "/api/admin/namespaces/:namespace/groups/:group/configs/:name"
	switch method + " " + path {
	case "POST /api/admin/login":
		return "auth.login"
	case "POST /api/admin/logout":
		return "auth.logout"
	case "POST /api/admin/password":
		return "auth.password"
	case "POST /api/admin/namespaces":
		return "namespace.create"
	case "DELETE /api/admin/namespaces/:namespace":
		return "namespace.delete"
	case "POST /api/admin/namespaces/:namespace/groups":
		return "group.create"
	case "DELETE /api/admin/namespaces/:namespace/groups/:group":
		return "group.delete"
	case "GET " + base:
		return "config.read"
	case "PUT " + base:
		return "config.publish"
	case "DELETE " + base:
		return "config.delete"
	case "PUT " + base + "/rules":
		return "config.rules"
	case "POST " + base + "/rollback":
		return "config.rollback"
	case "POST " + base + "/promote":
		return "config.promote"
	case "GET " + base + "/versions/:version":
		return "config.version.read"
	case "POST " + base + "/simulate":
		return "config.simulate"
	case "POST /api/admin/configs/batch-delete":
		return "config.batch-delete"
	case "POST /api/admin/configs/batch-move":
		return "config.batch-move"
	case "POST /api/admin/users":
		return "user.create"
	case "PUT /api/admin/users/:id":
		return "user.update"
	case "DELETE /api/admin/users/:id":
		return "user.delete"
	case "POST /api/admin/users/:id/password":
		return "user.password"
	}
	return ""
}
func auditText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func auditRecord(c *gin.Context) *storage.Audit {
	if value, ok := c.Get("audit"); ok {
		return value.(*storage.Audit)
	}
	return nil
}
func auditDetails(c *gin.Context, subject string, details any) {
	if a := auditRecord(c); a != nil {
		if subject != "" {
			a.Subject = auditText(subject, 512)
		}
		if details != nil {
			encoded, err := json.Marshal(details)
			if err == nil && len(encoded) <= 256<<10 {
				a.Details = encoded
			} else {
				a.Details = json.RawMessage(`{"truncated":true}`)
			}
		}
	}
}
func auditUser(c *gin.Context, user storage.User) {
	if a := auditRecord(c); a != nil {
		a.UserID = user.ID
		a.Username = user.Username
	}
}
func (s *Server) audit(c *gin.Context) {
	if s.options.ReadOnly {
		c.Next()
		return
	}
	action := auditAction(c.Request.Method, c.FullPath())
	if action == "" {
		c.Next()
		return
	}
	started := time.Now()
	subject := "控制台"
	if c.Param("namespace") != "" {
		subject = c.Param("namespace")
		if c.Param("group") != "" {
			subject += " / " + c.Param("group")
		}
		if c.Param("name") != "" {
			subject += " / " + c.Param("name")
		}
	}
	if c.Param("id") != "" {
		subject = "用户 / " + c.Param("id")
	}
	ip := c.ClientIP()
	if parsed, err := netip.ParseAddr(ip); err == nil {
		ip = parsed.Unmap().String()
	}
	a := storage.Audit{RequestID: uuid.NewString(), CreatedAt: started.UTC(), IP: auditText(ip, 45), Action: action, Subject: auditText(subject, 512), Method: c.Request.Method}
	c.Header("X-Request-ID", a.RequestID)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	id, err := s.store.BeginAudit(ctx, a)
	cancel()
	if err != nil {
		slog.Error("audit start failed", "request_id", a.RequestID)
		c.AbortWithStatusJSON(503, gin.H{"error": "audit storage unavailable"})
		return
	}
	a.ID = id
	c.Set("audit", &a)
	c.Next()
	a.Status = c.Writer.Status()
	a.DurationMS = time.Since(started).Milliseconds()
	a.Result = "failure"
	if a.Status >= 200 && a.Status < 300 {
		a.Result = "success"
	}
	// Completing the record survives cancellation after the mutation committed.
	ctx, cancel = context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	if err = s.store.FinishAudit(ctx, a); err != nil {
		slog.Error("audit completion failed", "request_id", a.RequestID, "audit_id", a.ID)
	}
}
func (s *Server) audits(c *gin.Context) {
	before, err := integer(c, "before", 0)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	limit, err := integer(c, "limit", 25)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	f := storage.AuditFilter{Before: before, Limit: int(limit), Username: c.Query("username"), IP: c.Query("ip"), Action: c.Query("action"), Result: c.Query("result")}
	for name, target := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if raw := c.Query(name); raw != "" {
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				s.respond(c, nil, config.ErrInvalid)
				return
			}
			*target = parsed
		}
	}
	page, err := s.store.Audits(c.Request.Context(), f)
	s.respond(c, page, err)
}
