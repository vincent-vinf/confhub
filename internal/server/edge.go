package server

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
)

// Login/logout and simulation change no managed data. All other non-read
// requests, including unknown paths, remain forbidden before routing.
func edgeReadRequest(c *gin.Context) bool {
	if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS" {
		return true
	}
	if c.Request.Method != "POST" {
		return false
	}
	switch c.FullPath() {
	case "/api/admin/login", "/api/admin/logout", "/api/admin/namespaces/:namespace/groups/:group/configs/:name/simulate":
		return true
	}
	return false
}

func (s *Server) me(c *gin.Context) {
	role := ""
	if s.options.Edge {
		role = "edge"
	}
	c.JSON(200, struct {
		storage.User
		Role     string `json:"role,omitempty"`
		ReadOnly bool   `json:"read_only,omitempty"`
	}{currentUser(c), role, s.options.Edge || s.options.ReadOnly})
}

func (s *Server) edgeRoutes(a *gin.RouterGroup) {
	a.GET("/edge/status", s.edgeStatus)
	a.GET("/namespaces", func(c *gin.Context) {
		names, err := s.store.MirrorNamespaces(c.Request.Context())
		visible := []string{}
		for _, name := range names {
			if currentUser(c).Allows("config.read", name, "") {
				visible = append(visible, name)
			}
		}
		s.respond(c, visible, err)
	})
	a.GET("/namespaces/:namespace/groups", func(c *gin.Context) {
		ns := c.Param("namespace")
		names, err := s.store.MirrorGroups(c.Request.Context(), ns)
		visible := []string{}
		for _, name := range names {
			if currentUser(c).Allows("config.read", ns, name) {
				visible = append(visible, name)
			}
		}
		s.respond(c, visible, err)
	})
	base := "/namespaces/:namespace/groups/:group/configs"
	a.GET(base, func(c *gin.Context) {
		limit, err := integer(c, "limit", 100)
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		rows, err := s.store.MirrorList(c.Request.Context(), c.Param("namespace"), c.Param("group"), c.Query("after"), int(limit))
		s.respond(c, rows, err)
	})
	a.GET(base+"/:name", func(c *gin.Context) {
		state, _, err := s.store.MirrorCurrent(c.Request.Context(), pathKey(c))
		s.respond(c, state, err)
	})
	a.POST(base+"/:name/simulate", func(c *gin.Context) {
		var body struct {
			Tags map[string]string `json:"tags"`
		}
		if !bind(c, &body) {
			return
		}
		if err := config.ValidateTags(body.Tags); err != nil {
			s.respond(c, nil, err)
			return
		}
		state, _, err := s.store.MirrorCurrent(c.Request.Context(), pathKey(c))
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		s.respond(c, config.Resolve(state, body.Tags), nil)
	})
	// Only the current main version is mirrored; never expose old normalized
	// tables as history after a generation replacement or deletion.
	a.GET(base+"/:name/versions", func(c *gin.Context) {
		before, err := integer(c, "before", 0)
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		limit, err := integer(c, "limit", 100)
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		if limit < 1 || limit > 100 {
			s.respond(c, nil, config.ErrInvalid)
			return
		}
		state, _, err := s.store.MirrorCurrent(c.Request.Context(), pathKey(c))
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		page := storage.VersionPage{Versions: []config.Version{}}
		if before == 0 || before > state.GlobalVersion {
			v := state.Versions[state.GlobalVersion]
			v.Content = ""
			v.References = []string{"global"}
			page.Versions = append(page.Versions, v)
		}
		s.respond(c, page, nil)
	})
	a.GET(base+"/:name/versions/:version", func(c *gin.Context) {
		number, err := strconv.ParseInt(c.Param("version"), 10, 64)
		if err != nil || number < 1 {
			s.respond(c, nil, fmt.Errorf("%w: invalid version", config.ErrInvalid))
			return
		}
		state, _, err := s.store.MirrorCurrent(c.Request.Context(), pathKey(c))
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		if number != state.GlobalVersion {
			s.respond(c, nil, config.ErrNotFound)
			return
		}
		s.respond(c, state.Versions[number], nil)
	})
}
