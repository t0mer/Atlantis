// Package web serves the embedded single-page UI built into dist/ (Vite
// outDir). Only dist/placeholder.html is tracked, so the binary builds and
// shows a notice before the UI exists. Unknown paths fall back to
// index.html for client-side routing.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the UI.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	if _, err := fs.Stat(root, "index.html"); err != nil {
		page, _ := fs.ReadFile(root, "placeholder.html")
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(page)
		})
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(root, p); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/" // SPA fallback
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
