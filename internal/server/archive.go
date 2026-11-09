package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vincent-vinf/confhub/internal/config"
	"github.com/vincent-vinf/confhub/internal/configarchive"
	"github.com/vincent-vinf/confhub/internal/storage"
)

func (s *Server) exportArchive(c *gin.Context) {
	ns, group := c.Query("namespace"), c.Query("group")
	if ns == "" || group == "" {
		s.respond(c, nil, fmt.Errorf("%w: namespace and group are required", config.ErrInvalid))
		return
	}
	if !s.require(c, "config.read", ns, group) {
		return
	}
	auditDetails(c, ns+" / "+group, map[string]any{"selected_count": len(c.QueryArray("name")), "search": auditText(c.Query("search"), 128)})
	items, err := s.store.ExportArchive(c.Request.Context(), ns, group, c.QueryArray("name"), c.Query("search"), s.options.Edge)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	data, err := configarchive.Encode(items)
	if err != nil {
		s.respond(c, nil, err)
		return
	}
	auditDetails(c, ns+" / "+group, map[string]any{"count": len(items), "selected_count": len(c.QueryArray("name")), "search": auditText(c.Query("search"), 128)})
	c.Header("Content-Disposition", `attachment; filename="nacos_config_export_`+time.Now().UTC().Format("20060102150405")+`.zip"`)
	c.Header("Cache-Control", "no-store")
	c.Data(200, "application/zip", data)
}

func (s *Server) importArchive(c *gin.Context, preview bool) {
	// Bound multipart memory and remove disk spills even on parsing errors.
	err := c.Request.ParseMultipartForm(1 << 20)
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if err != nil {
		s.respond(c, nil, fmt.Errorf("%w: invalid multipart upload (maximum request size 8 MiB)", config.ErrInvalid))
		return
	}
	ns, policy := c.PostForm("namespace"), c.DefaultPostForm("policy", "ABORT")
	auditDetails(c, ns, map[string]any{"policy": policy})
	if !s.require(c, "config.write", ns, "") {
		return
	}
	files := c.Request.MultipartForm.File["file"]
	if len(files) != 1 {
		s.respond(c, nil, fmt.Errorf("%w: exactly one ZIP file is required", config.ErrInvalid))
		return
	}
	f, err := files[0].Open()
	if err != nil {
		s.respond(c, nil, config.ErrInvalid)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, configarchive.MaxZIPBytes+1))
	if err != nil {
		s.respond(c, nil, config.ErrInvalid)
		return
	}
	p, err := configarchive.Parse(data)
	var itemErrors *configarchive.ItemErrors
	if err != nil && (!preview || !errors.As(err, &itemErrors)) {
		s.respond(c, nil, err)
		return
	}
	groups := []string{}
	seenGroups := map[string]bool{}
	for _, item := range p.Items {
		if !seenGroups[item.Group] {
			groups = append(groups, item.Group)
			seenGroups[item.Group] = true
		}
	}
	details := map[string]any{"policy": policy, "count": len(p.Items), "groups": groups}
	auditDetails(c, ns, details)
	for _, item := range p.Items {
		if !s.require(c, "config.write", ns, item.Group) {
			return
		}
	}
	if preview {
		plan, e := s.store.PreviewImport(c.Request.Context(), ns, policy, p, currentUser(c).SystemAdmin)
		s.respond(c, plan, e)
		return
	}
	if !confirm(c, c.PostForm("confirmed") == "true") {
		return
	}
	var plan storage.ImportPlan
	if err = json.Unmarshal([]byte(c.PostForm("plan")), &plan); err != nil || plan.Namespace != ns || plan.Policy != policy {
		s.respond(c, nil, fmt.Errorf("%w: preview does not match namespace or policy", config.ErrInvalid))
		return
	}
	result, err := s.store.Import(c.Request.Context(), p, plan, currentUser(c).SystemAdmin)
	if err == nil {
		details["created"], details["updated"], details["skipped"], details["unchanged"] = result.Created, result.Updated, result.Skipped, result.Unchanged
		auditDetails(c, ns, details)
		if s.hub != nil && result.Created+result.Updated > 0 {
			s.hub.Wake()
		}
	}
	s.respond(c, result, err)
}
