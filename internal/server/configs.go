package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/config"
)

type confirmedEdit struct {
	config.Edit
	Confirmed bool `json:"confirmed"`
}
type confirmedTarget struct {
	ExpectedID       string `json:"expected_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Confirmed        bool   `json:"confirmed"`
}

func confirm(c *gin.Context, confirmed bool) bool {
	if !confirmed {
		c.AbortWithStatusJSON(400, gin.H{"error": "confirm comparison and impact before submitting"})
		return false
	}
	return true
}
func pathKey(c *gin.Context) config.Key {
	return config.Key{Namespace: c.Param("namespace"), Group: c.Param("group"), Name: c.Param("name")}
}
func integer(c *gin.Context, name string, fallback int64) (int64, error) {
	raw := c.Query(name)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%w: invalid %s", config.ErrInvalid, name)
	}
	return v, nil
}
func (s *Server) configRoutes(a *gin.RouterGroup) {
	a.POST("/namespaces", func(c *gin.Context) {
		var b struct {
			Name string `json:"name"`
		}
		if !bind(c, &b) {
			return
		}
		err := s.store.CreateNamespace(c.Request.Context(), b.Name)
		s.respond(c, gin.H{"name": b.Name}, err)
	})
	a.DELETE("/namespaces/:namespace", func(c *gin.Context) {
		var b struct {
			Confirmed bool `json:"confirmed"`
		}
		if !bind(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		s.respond(c, gin.H{"deleted": true}, s.store.DeleteNamespace(c.Request.Context(), c.Param("namespace")))
	})
	a.GET("/namespaces/:namespace/groups", func(c *gin.Context) {
		v, err := s.store.Groups(c.Request.Context(), c.Param("namespace"))
		s.respond(c, v, err)
	})
	a.POST("/namespaces/:namespace/groups", func(c *gin.Context) {
		var b struct {
			Name string `json:"name"`
		}
		if !bind(c, &b) {
			return
		}
		err := s.store.CreateGroup(c.Request.Context(), c.Param("namespace"), b.Name)
		s.respond(c, gin.H{"name": b.Name}, err)
	})
	a.DELETE("/namespaces/:namespace/groups/:group", func(c *gin.Context) {
		var b struct {
			Confirmed bool `json:"confirmed"`
		}
		if !bind(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		s.respond(c, gin.H{"deleted": true}, s.store.DeleteGroup(c.Request.Context(), c.Param("namespace"), c.Param("group")))
	})
	base := "/namespaces/:namespace/groups/:group/configs"
	a.GET(base, func(c *gin.Context) {
		limit, err := integer(c, "limit", 100)
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		v, err := s.store.List(c.Request.Context(), c.Param("namespace"), c.Param("group"), c.Query("after"), int(limit))
		s.respond(c, v, err)
	})
	a.GET(base+"/:name", func(c *gin.Context) {
		v, err := s.store.Snapshot(c.Request.Context(), pathKey(c))
		s.respond(c, v, err)
	})
	a.PUT(base+"/:name", func(c *gin.Context) {
		var b confirmedEdit
		if !bindStrict(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		m, err := s.store.Save(c.Request.Context(), pathKey(c), b.Edit)
		s.mutated(c, "save", m, err)
	})
	a.DELETE(base+"/:name", func(c *gin.Context) {
		var b confirmedTarget
		if !bind(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		m, err := s.store.Delete(c.Request.Context(), pathKey(c), b.ExpectedID, b.ExpectedRevision)
		s.mutated(c, "delete", m, err)
	})
	a.PUT(base+"/:name/rules", func(c *gin.Context) {
		var b struct {
			confirmedTarget
			Rules  []config.Rule `json:"rules"`
			Source int64         `json:"source_version"`
		}
		if !bindStrict(c, &b) || !confirm(c, b.Confirmed) {
			return
		}
		m, err := s.store.SetRules(c.Request.Context(), pathKey(c), b.ExpectedID, b.ExpectedRevision, b.Rules, b.Source)
		s.mutated(c, "rules", m, err)
	})
	a.POST(base+"/:name/simulate", func(c *gin.Context) {
		var b struct {
			Tags map[string]string `json:"tags"`
		}
		if !bind(c, &b) {
			return
		}
		if err := config.ValidateTags(b.Tags); err != nil {
			s.respond(c, nil, err)
			return
		}
		state, err := s.store.Snapshot(c.Request.Context(), pathKey(c))
		if err != nil {
			s.respond(c, nil, err)
			return
		}
		s.respond(c, config.Resolve(state, b.Tags), nil)
	})
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
		v, err := s.store.History(c.Request.Context(), pathKey(c), before, int(limit))
		s.respond(c, v, err)
	})
	a.GET(base+"/:name/versions/:version", func(c *gin.Context) {
		number, err := strconv.ParseInt(c.Param("version"), 10, 64)
		if err != nil || number < 1 {
			s.respond(c, nil, fmt.Errorf("%w: invalid version", config.ErrInvalid))
			return
		}
		v, err := s.store.Version(c.Request.Context(), pathKey(c), number)
		s.respond(c, v, err)
	})
	for _, action := range []string{"rollback", "promote"} {
		a.POST(base+"/:name/"+action, func(c *gin.Context) {
			var b struct {
				confirmedTarget
				Source int64 `json:"source_version"`
			}
			if !bindStrict(c, &b) || !confirm(c, b.Confirmed) {
				return
			}
			target := ""
			if action == "promote" {
				target = "beta"
			}
			m, err := s.store.CopyVersion(c.Request.Context(), pathKey(c), b.ExpectedID, b.ExpectedRevision, b.Source, target, action == "rollback")
			s.mutated(c, action, m, err)
		})
	}
}
func (s *Server) mutated(c *gin.Context, action string, m config.Mutation, err error) {
	if err == nil && m.Changed {
		if s.hub != nil {
			s.hub.Wake()
		}
		slog.Info("configuration changed", "action", action, "id", m.State.ID, "key", m.State.Key, "revision", m.State.Revision, "sequence", m.Sequence)
	}
	s.respond(c, m, err)
}
func queryTags(c *gin.Context) (map[string]string, error) {
	tags := map[string]string{}
	if raw := c.Query("tags"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &tags); err != nil {
			return nil, fmt.Errorf("%w: tags must be a JSON string map", config.ErrInvalid)
		}
	}
	return tags, config.ValidateTags(tags)
}
func (s *Server) clientGet(c *gin.Context) {
	k := config.Key{Namespace: c.DefaultQuery("namespace", "public"), Group: c.DefaultQuery("group", "DEFAULT_GROUP"), Name: c.Query("name")}
	if err := k.Validate(); err != nil {
		s.respond(c, nil, err)
		return
	}
	tags, err := queryTags(c)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	var state *config.State
	var sequence int64
	if s.hub != nil {
		state, sequence, err = s.hub.Current(c.Request.Context(), k)
	} else {
		state, sequence, err = s.store.Current(c.Request.Context(), k)
	}
	if errors.Is(err, config.ErrNotFound) {
		c.JSON(404, config.Effective{Key: k, Sequence: sequence, Deleted: true})
		return
	}
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	s.respond(c, config.Resolve(state, tags), nil)
}
