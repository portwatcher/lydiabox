// Package webui embeds the web workstation into the lydiabox binary.
//
// Phase 5 builds web/ with Vite and copies web/dist into
// internal/webui/dist (`make web-build` does both). That directory is
// git-ignored except for dist/.gitkeep, which keeps the embed pattern valid
// when no UI has been built. When dist/index.html is absent, the committed
// placeholder/index.html is served instead, so `make build` works without
// Node.js.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist placeholder
var embedded embed.FS

// FS returns the UI file tree: the built dist when present, else the placeholder.
func FS() fs.FS {
	if _, err := fs.Stat(embedded, "dist/index.html"); err == nil {
		sub, _ := fs.Sub(embedded, "dist")
		return sub
	}
	sub, _ := fs.Sub(embedded, "placeholder")
	return sub
}

// Built reports whether a real UI build is embedded.
func Built() bool {
	_, err := fs.Stat(embedded, "dist/index.html")
	return err == nil
}

// Handler serves the SPA from fsys. Existing files are served directly;
// other paths without a file extension fall back to index.html so client-side
// routes (/studio, /files) load the app. Hashed assets under /assets/ are
// cached for a year; index.html is never cached.
func Handler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		fi, err := fs.Stat(fsys, name)
		if err != nil || fi.IsDir() {
			if path.Ext(name) != "" && name != "index.html" {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, r, fsys)
			return
		}
		if name == "index.html" {
			serveIndex(w, r, fsys)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "web UI is missing index.html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}
