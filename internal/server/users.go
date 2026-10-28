package server

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/storage"
)

func currentUser(c *gin.Context) storage.User { return c.MustGet("user").(storage.User) }
func (s *Server) require(c *gin.Context, action, namespace, group string) bool {
	if !currentUser(c).Allows(action, namespace, group) {
		s.respond(c, nil, storage.ErrForbidden)
		return false
	}
	return true
}

// Explicit operation classification keeps read-only POST simulation available.
// Unknown management operations fail closed.
func (s *Server) authorize(c *gin.Context) {
	path := c.FullPath()
	switch path {
	case "/api/admin/me", "/api/admin/logout", "/api/admin/password":
		return
	case "/api/admin/audits":
		s.require(c, "audit.read", "", "")
		return
	case "/api/admin/clients", "/api/admin/client-tags":
		s.require(c, "clients.read", "", "")
		return
	case "/api/admin/users", "/api/admin/users/:id", "/api/admin/users/:id/password":
		s.require(c, "users.manage", "", "")
		return
	case "/api/admin/configs/batch-delete", "/api/admin/configs/batch-move":
		return // Body targets checked together before mutation.
	case "/api/admin/namespaces", "/api/admin/namespaces/:namespace/groups":
		if c.Request.Method == "GET" {
			return
		} // Results filtered by scope.
		s.require(c, "organization.manage", "", "")
		return
	case "/api/admin/namespaces/:namespace", "/api/admin/namespaces/:namespace/groups/:group":
		s.require(c, "organization.manage", "", "")
		return
	}
	if (c.Param("namespace") != "" && c.Param("group") != "" && c.Param("name") != "") || path == "/api/admin/namespaces/:namespace/groups/:group/configs" {
		action := "config.write"
		if c.Request.Method == "GET" || path == "/api/admin/namespaces/:namespace/groups/:group/configs/:name/simulate" {
			action = "config.read"
		}
		s.require(c, action, c.Param("namespace"), c.Param("group"))
		return
	}
	s.respond(c, nil, storage.ErrForbidden)
}
func (s *Server) namespaces(c *gin.Context) {
	names, err := s.store.Namespaces(c.Request.Context())
	visible := []string{}
	for _, name := range names {
		if currentUser(c).Allows("config.read", name, "") {
			visible = append(visible, name)
		}
	}
	s.respond(c, visible, err)
}
func (s *Server) groups(c *gin.Context) {
	namespace := c.Param("namespace")
	if !s.require(c, "config.read", namespace, "") {
		return
	}
	groups, err := s.store.Groups(c.Request.Context(), namespace)
	visible := []string{}
	for _, group := range groups {
		if currentUser(c).Allows("config.read", namespace, group) {
			visible = append(visible, group)
		}
	}
	s.respond(c, visible, err)
}
func (s *Server) userRoutes(a *gin.RouterGroup) {
	a.GET("/me", func(c *gin.Context) {
		c.JSON(200, struct {
			storage.User
			ReadOnly bool `json:"read_only,omitempty"`
		}{currentUser(c), s.options.ReadOnly})
	})
	a.GET("/users", func(c *gin.Context) {
		limit, err := integer(c, "limit", 25)
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		users, err := s.store.Users(c.Request.Context(), c.Query("after"), int(limit))
		s.respond(c, users, err)
	})
	a.POST("/users", func(c *gin.Context) {
		var b struct {
			storage.UserInput
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !bindStrict(c, &b) {
			return
		}
		auditDetails(c, "用户 / "+b.Username, b.UserInput)
		u, err := s.store.CreateUser(c.Request.Context(), b.Username, b.Password, b.UserInput)
		if err == nil {
			auditDetails(c, "用户 / "+u.Username, b.UserInput)
			slog.Info("user changed", "actor_id", currentUser(c).ID, "user_id", u.ID, "method", c.Request.Method)
		}
		s.respond(c, u, err)
	})
	a.PUT("/users/:id", func(c *gin.Context) {
		var b struct {
			storage.UserInput
			ExpectedRevision int64 `json:"expected_revision"`
			Confirmed        bool  `json:"confirmed"`
		}
		if !bindStrict(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		auditDetails(c, "", b.UserInput)
		u, err := s.store.UpdateUser(c.Request.Context(), c.Param("id"), b.ExpectedRevision, b.UserInput)
		if err == nil {
			auditDetails(c, "用户 / "+u.Username, b.UserInput)
			slog.Info("user changed", "actor_id", currentUser(c).ID, "user_id", u.ID, "method", c.Request.Method)
		}
		s.respond(c, u, err)
	})
	a.DELETE("/users/:id", func(c *gin.Context) {
		var b struct {
			ExpectedRevision int64 `json:"expected_revision"`
			Confirmed        bool  `json:"confirmed"`
		}
		if !bindStrict(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		if target, err := s.store.User(c.Request.Context(), c.Param("id")); err == nil {
			auditDetails(c, "用户 / "+target.Username, nil)
		}
		err := s.store.DeleteUser(c.Request.Context(), c.Param("id"), b.ExpectedRevision)
		if err == nil {
			slog.Info("user deleted", "actor_id", currentUser(c).ID, "user_id", c.Param("id"))
		}
		s.respond(c, gin.H{"deleted": err == nil}, err)
	})
	a.POST("/users/:id/password", func(c *gin.Context) {
		var b struct {
			Password         string `json:"password"`
			ExpectedRevision int64  `json:"expected_revision"`
			Confirmed        bool   `json:"confirmed"`
		}
		if !bindStrict(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		if target, err := s.store.User(c.Request.Context(), c.Param("id")); err == nil {
			auditDetails(c, "用户 / "+target.Username, nil)
		}
		err := s.store.ResetPassword(c.Request.Context(), c.Param("id"), b.ExpectedRevision, b.Password)
		if err == nil {
			slog.Info("user password reset", "actor_id", currentUser(c).ID, "user_id", c.Param("id"))
		}
		s.respond(c, gin.H{"changed": err == nil}, err)
	})
}
