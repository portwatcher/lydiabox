package api

import (
	"net/http"

	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/storage"
)

// UnavailableGatewayRoutes answers /webdav and /s3 in the provider storage
// mode, where the guest trees are on the machine disk and no JuiceFS volume
// backs the gateways. The protection matches WebDAVRoutes and S3Routes.
type UnavailableGatewayRoutes struct{}

// ErrGatewayUnavailable is the message of the unavailable gateway routes.
const ErrGatewayUnavailable = "WebDAV and the /s3 gateway are not available in the provider storage mode; use the File API (/api/v1/fs)"

// RegisterRoutes implements RouteRegistrar.
func (UnavailableGatewayRoutes) RegisterRoutes(r Router) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotImplemented, httpx.CodeNotImpl, ErrGatewayUnavailable)
	})
	r.Handle(storage.WebDAVPrefix+"/", h, BasicChallenge())
	r.Handle(storage.WebDAVPrefix, h, BasicChallenge())
	r.Handle(storage.S3GatewayPrefix, h, Public())
	r.Handle(storage.S3GatewayPrefix+"/", h, Public())
}
