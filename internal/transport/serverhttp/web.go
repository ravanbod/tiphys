package serverhttp

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// webdist holds the built frontend (web/dist copied here by `make web`).
//
//go:embed all:webdist
var embeddedWeb embed.FS

// staticHandler serves the single-page app: real files as they are, any
// other path (client-side routes) as index.html.
func (h *Handler) staticHandler() gin.HandlerFunc {
	var root fs.FS
	if h.opts.WebDir != "" {
		root = os.DirFS(h.opts.WebDir)
	} else {
		sub, err := fs.Sub(embeddedWeb, "webdist")
		if err != nil {
			panic(err)
		}
		root = sub
	}
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/auth/") {
			abort(c, http.StatusNotFound, "not_found", "Not found.", nil)
			return
		}
		name := strings.TrimPrefix(path.Clean(p), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(root, name); err != nil || info.IsDir() {
			name = "index.html"
		}
		f, err := root.Open(name)
		if err != nil {
			c.String(http.StatusNotFound, "frontend is not built; run `make web`")
			return
		}
		defer f.Close()
		info, _ := f.Stat()
		if strings.HasPrefix(name, "assets/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		rs, ok := f.(readSeeker)
		if !ok {
			c.String(http.StatusInternalServerError, "cannot serve %s", name)
			return
		}
		http.ServeContent(c.Writer, c.Request, name, info.ModTime(), rs)
	}
}

type readSeeker interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
}
