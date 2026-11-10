package api

import "net/http"

// DesktopHandler serves the desktop routes (*desktop.Relay implements it).
type DesktopHandler interface {
	// ServeWS handles the signaling WebSocket.
	ServeWS(w http.ResponseWriter, r *http.Request)
	// ServeMode handles GET and PUT of the takeover mode.
	ServeMode(w http.ResponseWriter, r *http.Request)
}

// DesktopRoutes serves /api/v1/desktop/* (docs/api.md section 9).
type DesktopRoutes struct {
	Relay DesktopHandler
}

// RegisterRoutes implements RouteRegistrar.
func (d DesktopRoutes) RegisterRoutes(r Router) {
	r.HandleFunc("GET /api/v1/desktop/ws", d.Relay.ServeWS)
	r.HandleFunc("GET /api/v1/desktop/mode", d.Relay.ServeMode)
	r.HandleFunc("PUT /api/v1/desktop/mode", d.Relay.ServeMode)
}
