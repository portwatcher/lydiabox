package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// VMDetail is the VMStatus object of docs/api.md section 7.
type VMDetail struct {
	State              string         `json:"state"`
	Driver             string         `json:"driver"`
	CPUs               int            `json:"cpus"`
	MemMB              int            `json:"memMb"`
	GPU                string         `json:"gpu,omitempty"`
	BootedAt           time.Time      `json:"bootedAt"`
	Leases             map[string]int `json:"leases"`
	IdleTimeoutSeconds int64          `json:"idleTimeoutSeconds"`
	IdleSince          time.Time      `json:"idleSince"`
	LastError          string         `json:"lastError,omitempty"`
}

// ErrVMConflict marks a power transition that is not possible in the
// current state (409 conflict).
var ErrVMConflict = errors.New("vm: transition not possible in the current state")

// ErrVMUnavailable marks a failure to reach the VM or its guest daemon
// (503 unavailable).
var ErrVMUnavailable = errors.New("vm: unavailable")

// VMController is the VM power API (*vmm.Controller implements it).
type VMController interface {
	VMStatusSource
	Status(tenantID string) VMDetail
	EnsureRunning(ctx context.Context, tenantID string) error
	Hibernate(ctx context.Context, tenantID string) error
	Stop(ctx context.Context, tenantID string, force bool) error
}

// WakeTimeout bounds POST /api/v1/vm/wake.
const WakeTimeout = 120 * time.Second

// VMRoutes serves /api/v1/vm/* (docs/api.md section 7).
type VMRoutes struct {
	Controller VMController
}

// RegisterRoutes implements RouteRegistrar.
func (v VMRoutes) RegisterRoutes(r Router) {
	r.HandleFunc("GET /api/v1/vm/status", v.status)
	r.HandleFunc("POST /api/v1/vm/wake", v.wake)
	r.HandleFunc("POST /api/v1/vm/hibernate", v.hibernate)
	r.HandleFunc("POST /api/v1/vm/stop", v.stop)
}

func (v VMRoutes) status(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, v.Controller.Status(tenant.FromContext(r.Context())))
}

func (v VMRoutes) wake(w http.ResponseWriter, r *http.Request) {
	tid := tenant.FromContext(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), WakeTimeout)
	defer cancel()
	if err := v.Controller.EnsureRunning(ctx, tid); err != nil {
		writeVMError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v.Controller.Status(tid))
}

func (v VMRoutes) hibernate(w http.ResponseWriter, r *http.Request) {
	tid := tenant.FromContext(r.Context())
	if err := v.Controller.Hibernate(r.Context(), tid); err != nil {
		writeVMError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v.Controller.Status(tid))
}

// StopRequest is the optional body of POST /api/v1/vm/stop.
type StopRequest struct {
	Force bool `json:"force"`
}

func (v VMRoutes) stop(w http.ResponseWriter, r *http.Request) {
	var req StopRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.DefaultMaxBody))
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, httpx.CodeTooLarge, "request body too large")
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || dec.More() {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "invalid JSON body")
			return
		}
	}
	tid := tenant.FromContext(r.Context())
	if err := v.Controller.Stop(r.Context(), tid, req.Force); err != nil {
		writeVMError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, v.Controller.Status(tid))
}

// CodeTimeout is the error code of 504 responses.
const CodeTimeout = "timeout"

func writeVMError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrVMConflict):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		httpx.WriteError(w, http.StatusGatewayTimeout, CodeTimeout, "the VM did not reach RUNNING in time: "+err.Error())
	case errors.Is(err, ErrVMUnavailable):
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
	}
}
