package server

import (
	"log/slog"

	"github.com/gin-gonic/gin"
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
		slog.Info("configurations changed", "action", action, "count", result.Count, "sequence", result.Sequence)
	}
	s.respond(c, result, err)
}
