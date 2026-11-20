package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/harness"
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
	// Transition is "pausing" or "stopping" while that change runs (the
	// state stays RUNNING until it ends), else empty.
	Transition string `json:"transition,omitempty"`
	// StartBlockedUntil is set while the provider refuses a start or resume
	// (for example the InstaBox cooldown after the maximum run time);
	// LastError says why.
	StartBlockedUntil *time.Time `json:"startBlockedUntil,omitempty"`
}

// ErrVMConflict marks a power transition that is not possible in the
// current state (409 conflict).
var ErrVMConflict = errors.New("vm: transition not possible in the current state")

// ErrVMUnavailable marks a failure to reach the VM or its guest daemon
// (503 unavailable).
var ErrVMUnavailable = errors.New("vm: unavailable")

// CodeVMStopped is the WebSocket error code (desktop and terminal sockets)
// of a socket refused or ended because the user stopped the VM. Clients do
// not reconnect after it.
const CodeVMStopped = harness.CodeVMStopped

// CodeVMPaused is the WebSocket error code (desktop and terminal sockets)
// of a socket ended or refused because the VM pauses or is paused. Clients
// do not reconnect until the VM runs again.
const CodeVMPaused = harness.CodeVMPaused

// ErrVMStopped marks a request that needs the VM while the user has
// stopped it: only an explicit start (POST /api/v1/vm/wake) starts it
// again. It wraps ErrVMUnavailable (503 unavailable).
var ErrVMStopped = fmt.Errorf("%w: the VM was stopped; start it to use it", ErrVMUnavailable)

// ErrVMStarting marks a request that stopped its wait for a VM start or
// resume while the start continues. It wraps ErrVMUnavailable (503
// unavailable); the request log adds "Retry-After: 5"
// (httpx.NoteVMStarting), and the client sends the request again.
var ErrVMStarting = fmt.Errorf("%w: the VM is still starting; send the request again when it runs", ErrVMUnavailable)

// VMController is the VM power API (*vmm.Controller implements it).
type VMController interface {
	VMStatusSource
	Status(tenantID string) VMDetail
	// Start is an explicit start: it also starts a VM that the user
	// stopped (EnsureRunning does not).
	Start(ctx context.Context, tenantID string) error
	Hibernate(ctx context.Context, tenantID string) error
	Stop(ctx context.Context, tenantID string, force bool) error
}

// WakeTimeout bounds the wait of POST /api/v1/vm/wake. It is shorter than
// the 100 s after which a proxy in front of LydiaBox (Cloudflare) ends a
// request without a response. A start that takes longer continues: the
// route then answers 503 vm_starting with Retry-After (ErrVMStarting).
const WakeTimeout = RequestWakeWait

// RequestWakeWait is the longest time that an HTTP request waits for a VM
// start or resume (docs/api.md section 1).
const RequestWakeWait = 75 * time.Second

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
	if err := v.Controller.Start(ctx, tid); err != nil {
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
	// ResetRoot discards the persistent root after the stop (docker driver).
	ResetRoot bool `json:"resetRoot"`
}

// VMRootResetter is implemented by controllers that can discard the
// persistent root of a stopped VM (*vmm.Controller).
type VMRootResetter interface {
	ResetRoot(ctx context.Context, tenantID string) error
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
	rr, canReset := v.Controller.(VMRootResetter)
	if req.ResetRoot && !canReset {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "resetRoot is not supported")
		return
	}
	// The stop finishes when the client goes away (for example the page
	// closes): a shutdown cut in the middle leaves the VM half stopped.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), StopTimeout)
	defer cancel()
	err = v.Controller.Stop(ctx, tid, req.Force)
	// With resetRoot, an already stopped VM is not an error.
	if err != nil && !(req.ResetRoot && errors.Is(err, ErrVMConflict) && v.Controller.Status(tid).State == VMStopped) {
		writeVMError(w, err)
		return
	}
	if req.ResetRoot {
		if err := rr.ResetRoot(ctx, tid); err != nil {
			writeVMError(w, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, v.Controller.Status(tid))
}

// StopTimeout bounds POST /api/v1/vm/stop: the filesystem sync (30 s), the
// shutdown, and resetRoot.
const StopTimeout = 3 * time.Minute

// CodeTimeout is the error code of 504 responses.
const CodeTimeout = "timeout"

// noteVMStopped tells the request log that a 503 for a VM that the user
// stopped is an expected state, not a fault.
func noteVMStopped(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrVMStopped) || errors.Is(err, agentfs.ErrStopped) {
		httpx.NoteExpected(w)
	}
}

func writeVMError(w http.ResponseWriter, err error) {
	noteVMStopped(w, err)
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
