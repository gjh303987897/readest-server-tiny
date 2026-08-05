package server

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *Server) serveAdminUI(c *gin.Context) {
	requested := strings.TrimPrefix(c.Param("path"), "/")
	if requested == "" {
		requested = "index.html"
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+requested), "/")
	if cleaned != requested || !fs.ValidPath(cleaned) {
		c.Status(http.StatusNotFound)
		return
	}
	content, err := fs.ReadFile(s.adminUI, cleaned)
	if err != nil {
		if path.Ext(cleaned) != "" {
			c.Status(http.StatusNotFound)
			return
		}
		cleaned = "index.html"
		content, err = fs.ReadFile(s.adminUI, cleaned)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
	}
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	if strings.HasPrefix(cleaned, "assets/") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "no-store")
	}
	contentType := mime.TypeByExtension(path.Ext(cleaned))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Data(http.StatusOK, contentType, content)
}
