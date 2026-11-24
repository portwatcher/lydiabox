package api

import (
	"net/http"

	"github.com/portwatcher/lydiabox/internal/storage"
)

// S3Routes mounts the virtual S3 gateway at /s3 and /s3/. The routes are
// public: the S3 proxy verifies SigV4 signatures itself (access key
// "lydiabox", secret key LYDIABOX_AUTH_TOKEN), and the cookie and CSRF
// checks of the API would break S3 clients.
type S3Routes struct {
	Handler http.Handler
}

// RegisterRoutes implements RouteRegistrar.
func (s S3Routes) RegisterRoutes(r Router) {
	r.Handle(storage.S3GatewayPrefix, s.Handler, Public())
	r.Handle(storage.S3GatewayPrefix+"/", s.Handler, Public())
}
