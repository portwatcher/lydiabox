package api

import (
	"context"
	"net/http"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/tenant"
	"github.com/lydiabox/lydiabox/internal/version"
)

// VM power states reported by /api/v1/status and /api/v1/vm/status.
const (
	VMStopped = "STOPPED"
	VMBooting = "BOOTING"
	VMRunning = "RUNNING"
	VMPaused  = "PAUSED"
)

// VMStatus is the VM section of the status response.
type VMStatus struct {
	State  string `json:"state"`
	Driver string `json:"driver"`
}

// VMStatusSource reports the VM state. Phase 2 replaces StaticVMStatus with
// the vmm controller in internal/cli/up.go.
type VMStatusSource interface {
	VMStatus(ctx context.Context, tenantID string) (VMStatus, error)
}

// StaticVMStatus always reports the same state (Phase 1: STOPPED).
type StaticVMStatus VMStatus

// VMStatus implements VMStatusSource.
func (s StaticVMStatus) VMStatus(context.Context, string) (VMStatus, error) {
	return VMStatus(s), nil
}

// StorageStatusSource reports the storage state; storage.Provider implements it.
type StorageStatusSource interface {
	Status(tenantID string) storage.Status
}

// S3GatewayStatusSource reports the virtual S3 gateway; *storage.S3Gateway
// implements it.
type S3GatewayStatusSource interface {
	Status(tenantID string) storage.S3GatewayStatus
}

// StatusResponse is the body of GET /api/v1/status.
type StatusResponse struct {
	Version  version.Info   `json:"version"`
	TenantID string         `json:"tenantId"`
	VM       VMStatus       `json:"vm"`
	Storage  storage.Status `json:"storage"`
	Time     time.Time      `json:"time"`
}

// CoreRoutes registers health, status, and session authentication routes.
type CoreRoutes struct {
	Auth    *Authenticator
	VM      VMStatusSource
	Storage StorageStatusSource
	// S3Gateway fills storage.s3Gateway; nil reports a disabled gateway.
	S3Gateway S3GatewayStatusSource
}

// RegisterRoutes implements RouteRegistrar.
func (c CoreRoutes) RegisterRoutes(r Router) {
	r.HandleFunc("GET /api/v1/health", c.health, Public())
	r.HandleFunc("GET /api/v1/ready", c.ready, Public())
	r.HandleFunc("GET /api/v1/status", c.status)
	r.HandleFunc("POST /api/v1/auth/login", c.Auth.login, Public())
	r.HandleFunc("POST /api/v1/auth/logout", c.Auth.logout, Public())
	r.HandleFunc("GET /api/v1/auth/session", c.Auth.sessionStatus, Public())
}

func (c CoreRoutes) health(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ReadyResponse is the body of GET /api/v1/ready.
type ReadyResponse struct {
	Status   string `json:"status"`
	TenantID string `json:"tenantId"`
}

// ready answers when every subsystem has started (the server serves only
// after the start), with the tenant ID, so that a cell host can check that
// it reached the cell of the right tenant.
func (c CoreRoutes) ready(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, ReadyResponse{Status: "ready", TenantID: tenant.FromContext(r.Context())})
}

func (c CoreRoutes) status(w http.ResponseWriter, r *http.Request) {
	tid := tenant.FromContext(r.Context())
	resp := StatusResponse{Version: version.Get(), TenantID: tid, Time: time.Now().UTC()}
	if c.VM != nil {
		vm, err := c.VM.VMStatus(r.Context(), tid)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
			return
		}
		resp.VM = vm
	}
	if c.Storage != nil {
		resp.Storage = c.Storage.Status(tid)
	} else {
		resp.Storage = storage.Status{TenantID: tid}
	}
	if c.S3Gateway != nil {
		resp.Storage.S3Gateway = c.S3Gateway.Status(tid)
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}
