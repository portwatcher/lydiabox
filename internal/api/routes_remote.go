package api

import (
	"context"
	"net/http"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// TunnelStatus is the state of the cloudflared connector.
type TunnelStatus struct {
	// State is stopped, connecting, connected, or error.
	State     string    `json:"state"`
	LastError string    `json:"lastError,omitempty"`
	Restarts  int       `json:"restarts"`
	StartedAt time.Time `json:"startedAt"`
}

// RemoteAccessStatus is the body of GET /api/v1/remote-access.
type RemoteAccessStatus struct {
	// Provider is "cloudflare".
	Provider string `json:"provider"`
	// Configured is true when Cloudflare Access login is on.
	Configured bool     `json:"configured"`
	Hostname   string   `json:"hostname,omitempty"`
	TeamDomain string   `json:"teamDomain,omitempty"`
	Emails     []string `json:"emails,omitempty"`
	// ServiceTokenClientID is the CF-Access-Client-Id of the service token.
	ServiceTokenClientID string   `json:"serviceTokenClientId,omitempty"`
	BypassPaths          []string `json:"bypassPaths,omitempty"`
	// TURNKeyID is the stored Cloudflare Realtime TURN key. TURNInUse is
	// true when the desktop stream uses it (no WebRTC setting overrides it).
	TURNKeyID string `json:"turnKeyId,omitempty"`
	TURNInUse bool   `json:"turnInUse"`
	// Tunnel is nil when LydiaBox does not run cloudflared.
	Tunnel *TunnelStatus `json:"tunnel,omitempty"`
}

// RemoteAccessSource reports the remote access state.
type RemoteAccessSource interface {
	RemoteAccessStatus(ctx context.Context, tenantID string) RemoteAccessStatus
}

// RemoteAccessRoutes registers GET /api/v1/remote-access.
type RemoteAccessRoutes struct {
	Source RemoteAccessSource
}

// RegisterRoutes implements RouteRegistrar.
func (rr RemoteAccessRoutes) RegisterRoutes(r Router) {
	r.HandleFunc("GET /api/v1/remote-access", rr.status)
}

func (rr RemoteAccessRoutes) status(w http.ResponseWriter, r *http.Request) {
	st := RemoteAccessStatus{Provider: "cloudflare"}
	if rr.Source != nil {
		st = rr.Source.RemoteAccessStatus(r.Context(), tenant.FromContext(r.Context()))
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}
