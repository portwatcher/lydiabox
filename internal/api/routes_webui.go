package api

import (
	"net/http"
	"strings"

	"github.com/portwatcher/lydiabox/internal/httpx"
)

// WebUIRoutes serves the embedded single-page application at "/". It must be
// registered last; unknown /api/ paths get a JSON 404 instead of index.html.
type WebUIRoutes struct {
	Handler http.Handler
}

// RegisterRoutes implements RouteRegistrar.
func (u WebUIRoutes) RegisterRoutes(r Router) {
	r.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") || req.URL.Path == "/api" {
			httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "no such API route: "+req.Method+" "+req.URL.Path)
			return
		}
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpx.WriteError(w, http.StatusMethodNotAllowed, httpx.CodeBadRequest, "method not allowed")
			return
		}
		u.Handler.ServeHTTP(w, req)
	}), Public())
}
