package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/storage"
)

// This precedes auditing and authentication so even unknown management writes
// cannot reach a database mutation. Simulation is a read-only POST operation.
func (s *Server) readOnly(c *gin.Context) {
	if !s.options.ReadOnly {
		return
	}
	path := c.Request.URL.Path
	if path != "/api/admin" && !strings.HasPrefix(path, "/api/admin/") {
		return
	}
	if c.FullPath() == "/api/admin/clients" || c.FullPath() == "/api/admin/client-tags" {
		s.respond(c, nil, storage.ErrReadOnly)
		return
	}
	switch c.Request.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return
	case http.MethodPost:
		switch c.FullPath() {
		case "/api/admin/login", "/api/admin/logout", "/api/admin/namespaces/:namespace/groups/:group/configs/:name/simulate":
			return
		}
	}
	s.respond(c, nil, storage.ErrReadOnly)
}
