package api

import "github.com/portwatcher/lydiabox/internal/storage/files"

// FilesRoutes registers the offline File Manager API (/api/v1/fs).
type FilesRoutes struct {
	Handler *files.Handler
}

// RegisterRoutes implements RouteRegistrar.
func (f FilesRoutes) RegisterRoutes(r Router) {
	h := f.Handler
	r.HandleFunc("GET /api/v1/fs/roots", h.Roots)
	r.HandleFunc("GET /api/v1/fs/{root}/list", h.List)
	r.HandleFunc("GET /api/v1/fs/{root}/stat", h.Stat)
	r.HandleFunc("GET /api/v1/fs/{root}/text", h.ReadText)
	r.HandleFunc("PUT /api/v1/fs/{root}/text", h.SaveText)
	r.HandleFunc("GET /api/v1/fs/{root}/download", h.Download)
	r.HandleFunc("POST /api/v1/fs/{root}/upload", h.Upload)
	r.HandleFunc("POST /api/v1/fs/{root}/mkdir", h.Mkdir)
	r.HandleFunc("POST /api/v1/fs/{root}/rename", h.Rename)
	r.HandleFunc("DELETE /api/v1/fs/{root}/entry", h.Delete)
}
