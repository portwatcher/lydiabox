package api

import (
	"net/http"

	"github.com/portwatcher/lydiabox/internal/storage"
)

// WebDAVRoutes mounts the WebDAV gateway at /webdav/. Clients authenticate
// with Basic Auth (user "lydiabox", password LYDIABOX_AUTH_TOKEN).
type WebDAVRoutes struct {
	Handler http.Handler
}

// RegisterRoutes implements RouteRegistrar.
func (d WebDAVRoutes) RegisterRoutes(r Router) {
	r.Handle(storage.WebDAVPrefix+"/", d.Handler, BasicChallenge())
	r.Handle(storage.WebDAVPrefix, http.RedirectHandler(storage.WebDAVPrefix+"/", http.StatusMovedPermanently), BasicChallenge())
}
