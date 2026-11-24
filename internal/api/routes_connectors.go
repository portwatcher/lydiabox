package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/portwatcher/lydiabox/internal/connector"
	"github.com/portwatcher/lydiabox/internal/httpx"
	"github.com/portwatcher/lydiabox/internal/tenant"
)

// ConnectorRoutes registers the connector API (docs/api.md section 11) and
// the host connector MCP server at /mcp/connectors (section 5b).
type ConnectorRoutes struct {
	Manager *connector.Manager
	Logger  *slog.Logger
	// MCP is the connector broker of the tenant (it serves MCP at /mcp, the
	// handler that the guest reaches at 127.0.0.1:8911/mcp). Nil skips the
	// /mcp/connectors routes.
	MCP http.Handler
}

// RegisterRoutes implements RouteRegistrar.
func (c ConnectorRoutes) RegisterRoutes(r Router) {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	r.HandleFunc("GET /api/v1/connectors", c.list)
	r.HandleFunc("GET /api/v1/connectors/audit", c.audit)
	r.HandleFunc("GET /api/v1/connectors/{provider}", c.get)
	r.HandleFunc("PUT /api/v1/connectors/{provider}/client", c.putClient)
	r.HandleFunc("POST /api/v1/connectors/{provider}/authorize", c.authorize)
	r.HandleFunc("GET /api/v1/connectors/{provider}/callback", c.callback, Public())
	r.HandleFunc("POST /api/v1/connectors/{provider}/token", c.bindToken)
	r.HandleFunc("DELETE /api/v1/connectors/{provider}", c.disconnect)
	if c.MCP != nil {
		// POST is JSON-RPC; GET gets the broker's 405 (no SSE stream).
		r.HandleFunc("POST /mcp/connectors", c.mcp)
		r.HandleFunc("GET /mcp/connectors", c.mcp)
	}
}

// mcp serves /mcp/connectors with the broker's /mcp handler.
func (c ConnectorRoutes) mcp(w http.ResponseWriter, r *http.Request) {
	out := r.Clone(r.Context())
	out.URL.Path = "/mcp"
	out.URL.RawPath = ""
	out.RequestURI = ""
	c.MCP.ServeHTTP(w, out)
}

func (c ConnectorRoutes) writeErr(w http.ResponseWriter, err error) {
	var ae *connector.APIError
	switch {
	case errors.Is(err, connector.ErrUnknownProvider), errors.Is(err, connector.ErrNotConnected):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	case errors.Is(err, connector.ErrInvalidInput), errors.Is(err, connector.ErrTokenRejected),
		errors.Is(err, connector.ErrAmbiguous):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
	case errors.Is(err, connector.ErrNotConfigured):
		httpx.WriteError(w, http.StatusConflict, httpx.CodeConflict, err.Error())
	case errors.As(err, &ae):
		httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
	case errors.Is(err, context.Canceled):
		// The client left (for example a page change during the load): not a
		// server error.
		httpx.WriteError(w, httpx.StatusClientClosedRequest, httpx.CodeCanceled, "the request was canceled")
	default:
		httpx.NoteCause(w, err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
	}
}

func (c ConnectorRoutes) list(w http.ResponseWriter, r *http.Request) {
	list, err := c.Manager.List(r.Context(), tenant.FromContext(r.Context()))
	if err != nil {
		c.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"connectors": list})
}

func (c ConnectorRoutes) get(w http.ResponseWriter, r *http.Request) {
	conn, err := c.Manager.Get(r.Context(), tenant.FromContext(r.Context()), r.PathValue("provider"))
	if err != nil {
		c.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, conn)
}

// ClientRequest is the body of PUT /api/v1/connectors/{provider}/client.
type ClientRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

func (c ConnectorRoutes) putClient(w http.ResponseWriter, r *http.Request) {
	var req ClientRequest
	if err := httpx.DecodeJSON(w, r, 64<<10, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	conn, err := c.Manager.SetClient(r.Context(), tenant.FromContext(r.Context()), r.PathValue("provider"), req.ClientID, req.ClientSecret)
	if err != nil {
		c.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, conn)
}

// AuthorizeRequest is the optional body of POST .../authorize.
type AuthorizeRequest struct {
	Scopes   []string `json:"scopes"`
	ReturnTo string   `json:"returnTo"`
}

// requestOrigin is the scheme and host the browser used, which is the base
// of the OAuth redirect URI.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func sessionBinding(r *http.Request) string {
	if ck, err := r.Cookie(SessionCookie); err == nil {
		return connector.SessionBinding(ck.Value)
	}
	return ""
}

func (c ConnectorRoutes) authorize(w http.ResponseWriter, r *http.Request) {
	var req AuthorizeRequest
	if err := httpx.DecodeJSON(w, r, 64<<10, &req); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	provider := r.PathValue("provider")
	res, err := c.Manager.Authorize(r.Context(), tenant.FromContext(r.Context()), provider, connector.AuthorizeRequest{
		Scopes: req.Scopes, ReturnTo: req.ReturnTo,
		RedirectURI:    requestOrigin(r) + "/api/v1/connectors/" + url.PathEscape(provider) + "/callback",
		SessionBinding: sessionBinding(r),
	})
	if err != nil {
		c.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// withQuery appends key=value to a same-origin path.
func withQuery(path string, kv ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	frag := ""
	if i := strings.IndexByte(path, '#'); i >= 0 {
		path, frag = path[:i], path[i:]
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + q.Encode() + frag
}

func (c ConnectorRoutes) callback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	q := r.URL.Query()
	returnTo, err := c.Manager.Callback(r.Context(), tenant.FromContext(r.Context()), provider, connector.CallbackRequest{
		Code: q.Get("code"), State: q.Get("state"), Error: q.Get("error"), SessionBinding: sessionBinding(r),
	})
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		code := "server_error"
		var ce *connector.CallbackError
		if errors.As(err, &ce) {
			code = ce.Code
		}
		c.Logger.Warn("connector authorization failed", "provider", provider, "error", code, "err", err)
		http.Redirect(w, r, withQuery(returnTo, "error", code, "provider", provider), http.StatusFound)
		return
	}
	http.Redirect(w, r, withQuery(returnTo, "connected", provider), http.StatusFound)
}

// TokenRequest is the body of POST .../token.
type TokenRequest struct {
	Token        string   `json:"token"`
	AccountLabel string   `json:"accountLabel"`
	Scopes       []string `json:"scopes"`
}

func (c ConnectorRoutes) bindToken(w http.ResponseWriter, r *http.Request) {
	var req TokenRequest
	if err := httpx.DecodeJSON(w, r, 64<<10, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	conn, err := c.Manager.BindToken(r.Context(), tenant.FromContext(r.Context()), r.PathValue("provider"), connector.BindTokenRequest{
		Token: req.Token, AccountLabel: req.AccountLabel, Scopes: req.Scopes,
	})
	if err != nil {
		c.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, conn)
}

func (c ConnectorRoutes) disconnect(w http.ResponseWriter, r *http.Request) {
	err := c.Manager.Disconnect(r.Context(), tenant.FromContext(r.Context()), r.PathValue("provider"), r.URL.Query().Get("account"))
	if err != nil {
		c.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c ConnectorRoutes) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var before int64
	limit := 0
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "before must be a non-negative integer")
			return
		}
		before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	page, err := c.Manager.Audit(r.Context(), tenant.FromContext(r.Context()), q.Get("provider"), before, limit)
	if err != nil {
		c.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}
