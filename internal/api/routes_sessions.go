package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// SessionsRoutes serves /api/v1/sessions* (docs/api.md section 8).
type SessionsRoutes struct {
	Manager *harness.Manager
}

// RegisterRoutes implements RouteRegistrar.
func (s SessionsRoutes) RegisterRoutes(r Router) {
	r.HandleFunc("GET /api/v1/sessions", s.list)
	r.HandleFunc("POST /api/v1/sessions", s.create)
	r.HandleFunc("DELETE /api/v1/sessions/{id}", s.remove)
	r.HandleFunc("GET /api/v1/sessions/{id}/ws", s.ws)
}

// CodeNotInstalled answers a session start whose harness program is not
// installed in the VM (409).
const CodeNotInstalled = "not_installed"

// writeHarnessError maps harness error classes to the error envelope.
func writeHarnessError(w http.ResponseWriter, err error) {
	status, code := harnessStatus(err)
	noteVMStopped(w, err)
	httpx.WriteError(w, status, code, err.Error())
}

func harnessStatus(err error) (int, string) {
	switch {
	case errors.Is(err, harness.ErrNotInstalled):
		return http.StatusConflict, CodeNotInstalled
	case errors.Is(err, harness.ErrNotFound):
		return http.StatusNotFound, httpx.CodeNotFound
	case errors.Is(err, harness.ErrBadRequest):
		return http.StatusBadRequest, httpx.CodeBadRequest
	case errors.Is(err, harness.ErrConflict):
		return http.StatusConflict, httpx.CodeConflict
	case errors.Is(err, harness.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, httpx.CodeUnavailable
	case errors.Is(err, context.Canceled):
		// The client closed the request (for example a page reload while
		// the guest discovery waited for the machine): not a server error.
		return httpx.StatusClientClosedRequest, httpx.CodeCanceled
	}
	return http.StatusInternalServerError, httpx.CodeInternal
}

func (s SessionsRoutes) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := harness.Filter{Harness: q.Get("harness"), Workspace: q.Get("workspace")}
	if f.Workspace != "" {
		ws, err := harness.CleanGuestDir(f.Workspace)
		if err != nil {
			writeHarnessError(w, err)
			return
		}
		f.Workspace = ws
	}
	list, err := s.Manager.ListSessions(r.Context(), tenant.FromContext(r.Context()), f)
	if err != nil {
		writeHarnessError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s SessionsRoutes) create(w http.ResponseWriter, r *http.Request) {
	var req harness.CreateRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	sess, err := s.Manager.Create(r.Context(), tenant.FromContext(r.Context()), req)
	if err != nil {
		writeHarnessError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, sess)
}

func (s SessionsRoutes) remove(w http.ResponseWriter, r *http.Request) {
	purge := false
	switch v := r.URL.Query().Get("purge"); v {
	case "", "false", "0":
	case "true", "1":
		purge = true
	default:
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "purge must be true or false")
		return
	}
	if err := s.Manager.Delete(r.Context(), tenant.FromContext(r.Context()), r.PathValue("id"), purge); err != nil {
		writeHarnessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
