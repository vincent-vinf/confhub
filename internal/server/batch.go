package server

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/storage"
)

func (s *Server) batchConfigs(c *gin.Context, move bool) {
	var body struct {
		Configs   []storage.ConfigTarget `json:"configs"`
		Namespace string                 `json:"namespace"`
		Group     string                 `json:"group"`
		Confirmed bool                   `json:"confirmed"`
	}
	if !bindStrict(c, &body) || !confirm(c, body.Confirmed) {
		return
	}
	keys := make([]config.Key, 0, min(len(body.Configs), 200))
	for _, target := range body.Configs[:min(len(body.Configs), 200)] {
		keys = append(keys, config.Key{Namespace: auditText(target.Key.Namespace, 128), Group: auditText(target.Key.Group, 128), Name: auditText(target.Key.Name, 128)})
	}
	auditDetails(c, "批量配置", map[string]any{"configs": keys, "count": len(body.Configs), "target_namespace": auditText(body.Namespace, 128), "target_group": auditText(body.Group, 128)})
	for _, target := range body.Configs {
		if !s.require(c, "config.write", target.Key.Namespace, target.Key.Group) {
			return
		}
	}
	if move && !s.require(c, "config.write", body.Namespace, body.Group) {
		return
	}
	var result storage.BatchMutation
	var err error
	action := "batch-delete"
	if move {
		action = "batch-move"
		result, err = s.store.MoveMany(c.Request.Context(), body.Configs, body.Namespace, body.Group)
	} else {
		result, err = s.store.DeleteMany(c.Request.Context(), body.Configs)
	}
	if err == nil {
		if s.hub != nil {
			s.hub.Wake()
		}
		slog.Info("configurations changed", "user_id", currentUser(c).ID, "action", action, "count", result.Count, "sequence", result.Sequence)
	}
	s.respond(c, result, err)
}
