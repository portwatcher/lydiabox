package api

import (
	"net/http"

	"github.com/portwatcher/lydiabox/internal/httpx"
	"github.com/portwatcher/lydiabox/internal/magpie"
)

// registerSubscriptions serves /api/v1/models/subscriptions* (docs/api.md
// section 10): harness subscription sign-ins that magpie keeps on the host.
func (m ModelsRoutes) registerSubscriptions(r Router) {
	r.HandleFunc("GET /api/v1/models/subscriptions", m.listSubscriptions)
	r.HandleFunc("POST /api/v1/models/subscriptions/logins", m.startLogin)
	r.HandleFunc("GET /api/v1/models/subscriptions/logins/{id}", m.loginStatus)
	r.HandleFunc("POST /api/v1/models/subscriptions/logins/{id}/code", m.submitLoginCode)
	r.HandleFunc("DELETE /api/v1/models/subscriptions/logins/{id}", m.cancelLogin)
	r.HandleFunc("DELETE /api/v1/models/subscriptions/{kind}", m.removeAccount)
}

func (m ModelsRoutes) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := m.Service.ListSubscriptions(r.Context())
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (m ModelsRoutes) startLogin(w http.ResponseWriter, r *http.Request) {
	var req magpie.StartLoginRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	login, err := m.Service.StartLogin(r.Context(), req)
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, login)
}

func (m ModelsRoutes) loginStatus(w http.ResponseWriter, r *http.Request) {
	login, err := m.Service.LoginStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, login)
}

func (m ModelsRoutes) submitLoginCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	login, err := m.Service.SubmitLoginCode(r.Context(), r.PathValue("id"), req.Code)
	if err != nil {
		writeMagpieError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, login)
}

func (m ModelsRoutes) cancelLogin(w http.ResponseWriter, r *http.Request) {
	if err := m.Service.CancelLogin(r.Context(), r.PathValue("id")); err != nil {
		writeMagpieError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m ModelsRoutes) removeAccount(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")
	if account == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "account is required")
		return
	}
	if err := m.Service.RemoveAccount(r.Context(), r.PathValue("kind"), account); err != nil {
		writeMagpieError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
