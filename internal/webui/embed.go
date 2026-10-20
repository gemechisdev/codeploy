// Package webui serves the embedded SPA (web/dist copied to internal/webui/dist by `make build`) with SPA fallback.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

func Handler() http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := sub.Open(p); err == nil {
			f.Close()
			files.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			http.Error(w, "web UI not built: run `make build`", http.StatusNotFound)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
