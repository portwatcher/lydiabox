package api

import (
	"errors"
	"net/http"

	"github.com/portwatcher/lydiabox/internal/httpx"
	"github.com/portwatcher/lydiabox/internal/magpie"
	"github.com/portwatcher/lydiabox/internal/tenant"
)

// ModelsRoutes serves /api/v1/models* (docs/api.md section 10).
type ModelsRoutes struct {
	Service *magpie.Service
}

// RegisterRoutes implements RouteRegistrar.
func (m ModelsRoutes) RegisterRoutes(r Router) {
	r.HandleFunc("GET /api/v1/models/gateway", m.gateway)
	r.HandleFunc("GET /api/v1/models/providers", m.listProviders)
	r.HandleFunc("POST /api/v1/models/providers", m.createProvider)
	r.HandleFunc("PATCH /api/v1/models/providers/{id}", m.updateProvider)
	r.HandleFunc("DELETE /api/v1/models/providers/{id}", m.deleteProvider)
	r.HandleFunc("GET /api/v1/models", m.listModels)
	r.HandleFunc("GET /api/v1/models/routing", m.listRouting)
	r.HandleFunc("PUT /api/v1/models/routing", m.putRouting)
	r.HandleFunc("GET /api/v1/models/bindings", m.listBindings)
	r.HandleFunc("PUT /api/v1/models/bindings/{harness}", m.putBinding)
	m.registerSubscriptions(r)
}

func writeMagpieError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, magpie.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, magpie.ErrBadRequest):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, magpie.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, err.Error())
	case errors.Is(err, magpie.ErrUnavailable):
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, err.Error())
	}
}

func (m ModelsRoutes) gateway(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.Service.GatewayInfo())
}

func (m ModelsRoutes) listProviders(w http.ResponseWriter, r *http.Request) {
	ps, err := m.Service.ListProviders(r.Context())
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"providers": ps})
}

func (m ModelsRoutes) createProvider(w http.ResponseWriter, r *http.Request) {
	var req magpie.CreateProviderRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	p, err := m.Service.CreateProvider(r.Context(), req)
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p)
}

func (m ModelsRoutes) updateProvider(w http.ResponseWriter, r *http.Request) {
	var req magpie.PatchProviderRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	p, err := m.Service.UpdateProvider(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (m ModelsRoutes) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := m.Service.DeleteProvider(r.Context(), r.PathValue("id")); err != nil {
		writeMagpieError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m ModelsRoutes) listModels(w http.ResponseWriter, r *http.Request) {
	ms, err := m.Service.ListModels(r.Context())
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": ms})
}

func (m ModelsRoutes) listRouting(w http.ResponseWriter, r *http.Request) {
	gs, err := m.Service.ListRouting(r.Context())
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"groups": gs})
}

func (m ModelsRoutes) putRouting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Groups []magpie.RoutingGroup `json:"groups"`
	}
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if req.Groups == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "groups is required")
		return
	}
	gs, err := m.Service.ReplaceRouting(r.Context(), req.Groups)
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"groups": gs})
}

func (m ModelsRoutes) listBindings(w http.ResponseWriter, r *http.Request) {
	bs, err := m.Service.Bindings.List(r.Context(), tenant.FromContext(r.Context()))
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"bindings": bs})
}

func (m ModelsRoutes) putBinding(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model *string `json:"model"`
	}
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if req.Model == nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "model is required (empty string unbinds)")
		return
	}
	b, err := m.Service.Bindings.Bind(r.Context(), tenant.FromContext(r.Context()), r.PathValue("harness"), *req.Model)
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, b)
}
