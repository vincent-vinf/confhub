package server

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *Server) static(c *gin.Context) {
	p := path.Clean("/" + c.Request.URL.Path)
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(404)
		return
	}
	if p == "/api" || strings.HasPrefix(p, "/api/") || p == "/ws" || strings.HasPrefix(p, "/ws/") {
		c.Status(404)
		return
	}
	if s.options.StaticDir == "" {
		c.Status(404)
		return
	}
	// OpenRoot confines symlinks and traversal to the deployment's build directory.
	root, err := os.OpenRoot(s.options.StaticDir)
	if err != nil {
		c.Status(404)
		return
	}
	defer root.Close()
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		name = "index.html"
	}
	file, err := root.Open(name)
	if err != nil {
		// Configuration names commonly contain a format extension. A detail-page
		// deep link is still an SPA route; missing asset files must remain 404.
		parts := strings.Split(strings.Trim(p, "/"), "/")
		configPage := len(parts) == 4 && parts[0] == "configs"
		if (path.Ext(p) != "" && !configPage) || p == "/assets" || strings.HasPrefix(p, "/assets/") {
			c.Status(404)
			return
		}
		file, err = root.Open("index.html")
		name = "index.html"
	}
	if err != nil {
		c.Status(404)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		c.Status(404)
		return
	}
	http.ServeContent(c.Writer, c.Request, filepath.Base(name), info.ModTime(), file)
}
