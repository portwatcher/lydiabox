package magpie

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPAdmin talks to a magpie process. The gateway (default
// 127.0.0.1:3425) serves /api/hello and /v1/models; the admin web API
// (`magpie serve --web`) serves providers, routing groups, and caller keys
// and is authenticated with the magpie web key in the
// "magpie_web_<port>" cookie.
type HTTPAdmin struct {
	GatewayURL string
	WebURL     string
	WebKey     string
	Client     *http.Client
}

// NewHTTPAdmin returns a client for the gateway and admin web addresses
// ("host:port").
func NewHTTPAdmin(gatewayAddr, webAddr, webKey string) *HTTPAdmin {
	return &HTTPAdmin{
		GatewayURL: "http://" + gatewayAddr,
		WebURL:     "http://" + webAddr,
		WebKey:     webKey,
		Client:     &http.Client{Timeout: 20 * time.Second},
	}
}

func (a *HTTPAdmin) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *HTTPAdmin) cookieName() string {
	u, err := url.Parse(a.WebURL)
	if err != nil {
		return "magpie_web"
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	return "magpie_web_" + port
}

// do sends one request and decodes a JSON response into out (when non-nil).
func (a *HTTPAdmin) do(ctx context.Context, method, base, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if base == a.WebURL && a.WebKey != "" {
		req.AddCookie(&http.Cookie{Name: a.cookieName(), Value: a.WebKey})
	}
	resp, err := a.client().Do(req)
	if err != nil {
		// Any transport failure (refused, timeout, reset) means magpie is
		// not reachable right now.
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("%w: read %s: %v", ErrUnavailable, path, err)
	}
	if resp.StatusCode >= 300 {
		return remoteError(resp.StatusCode, path, data)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("magpie %s: decode response: %w", path, err)
	}
	return nil
}

// remoteError turns a magpie error response ({"error": "..."}) into an error
// of the right class.
func remoteError(status int, path string, data []byte) error {
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		msg = body.Error
	}
	if len(msg) > 512 {
		msg = msg[:512]
	}
	switch {
	case body.Code == "signed_in" || body.Code == "signed_in_only":
		// magpie keeps the account its own harness copy is signed in to.
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	case status == http.StatusNotFound:
		return fmt.Errorf("%w: magpie %s: %s", ErrNotFound, path, msg)
	case status == http.StatusBadRequest:
		if strings.Contains(strings.ToLower(msg), "not found") {
			return fmt.Errorf("%w: %s", ErrNotFound, msg)
		}
		return fmt.Errorf("%w: %s", ErrBadRequest, msg)
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status >= 500:
		return fmt.Errorf("%w: magpie %s: HTTP %d: %s", ErrUnavailable, path, status, msg)
	}
	return fmt.Errorf("magpie %s: HTTP %d: %s", path, status, msg)
}

// Hello implements AdminAPI.
func (a *HTTPAdmin) Hello(ctx context.Context) (Hello, error) {
	var h Hello
	err := a.do(ctx, http.MethodGet, a.GatewayURL, "/api/hello", nil, &h)
	return h, err
}

type providerWire struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Preset    string `json:"preset"`
	Chat      string `json:"chat"`
	Responses string `json:"responses"`
	Anthropic string `json:"anthropic"`
	Gemini    string `json:"gemini"`
	Off       bool   `json:"off"`
	Ready     bool   `json:"ready"`
	Key       struct {
		Set bool `json:"set"`
	} `json:"key"`
	Models    []json.RawMessage `json:"models"`
	ListError string            `json:"listError"`
	Error     string            `json:"error"`
	Account   *accountWire      `json:"account"`
}

// ListProviders implements AdminAPI.
func (a *HTTPAdmin) ListProviders(ctx context.Context) ([]RemoteProvider, error) {
	var resp struct {
		Providers []providerWire `json:"providers"`
	}
	if err := a.do(ctx, http.MethodGet, a.WebURL, "/api/providers", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]RemoteProvider, 0, len(resp.Providers))
	for _, p := range resp.Providers {
		rp := RemoteProvider{ID: p.ID, Name: p.Name, Preset: p.Preset, Chat: p.Chat, Responses: p.Responses,
			Anthropic: p.Anthropic, Gemini: p.Gemini, Off: p.Off, HasKey: p.Key.Set, Ready: p.Ready,
			Models: []string{}, Error: firstNonEmpty(p.Error, p.ListError), Account: p.Account.remote()}
		for _, m := range p.Models {
			if id := modelIDOf(m); id != "" {
				rp.Models = append(rp.Models, id)
			}
		}
		out = append(out, rp)
	}
	return out, nil
}

// modelIDOf accepts a model as a string or as {"id": "..."}.
func modelIDOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.ID
	}
	return ""
}

// SaveProvider implements AdminAPI (POST /api/provider/save).
func (a *HTTPAdmin) SaveProvider(ctx context.Context, in ProviderInput) error {
	body := map[string]any{"id": in.ID}
	if in.New {
		body["new"] = true
	}
	if in.Preset != "" {
		body["preset"] = in.Preset
	}
	setIf := func(k string, v *string) {
		if v != nil {
			body[k] = *v
		}
	}
	setIf("name", in.Name)
	setIf("chat", in.Chat)
	setIf("anthropic", in.Anthropic)
	setIf("gemini", in.Gemini)
	setIf("key", in.Key)
	if !in.New && in.Key == nil {
		// magpie replaces the provider with the saved body; keep its key.
		var k struct {
			Key string `json:"key"`
		}
		if err := a.do(ctx, http.MethodPost, a.WebURL, "/api/provider/key", map[string]any{"id": in.ID}, &k); err != nil {
			return err
		}
		body["key"] = k.Key
	}
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/provider/save", body, nil)
}

// SetProviderOff implements AdminAPI.
func (a *HTTPAdmin) SetProviderOff(ctx context.Context, id string, off bool) error {
	action := "on"
	if off {
		action = "off"
	}
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/provider/"+action, map[string]any{"id": id}, nil)
}

// DeleteProvider implements AdminAPI.
func (a *HTTPAdmin) DeleteProvider(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/provider/delete", map[string]any{"id": id}, nil)
}

// ListModels implements AdminAPI (GET /v1/models on the gateway).
func (a *HTTPAdmin) ListModels(ctx context.Context) ([]RemoteModel, error) {
	var resp struct {
		Data []struct {
			ID               string `json:"id"`
			OwnedBy          string `json:"owned_by"`
			ContextWindow    int64  `json:"context_window"`
			ContextLength    int64  `json:"context_length"`
			MaxContextLength int64  `json:"max_context_length"`
		} `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, a.GatewayURL, "/v1/models", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]RemoteModel, 0, len(resp.Data))
	for _, m := range resp.Data {
		cw := m.ContextWindow
		if cw == 0 {
			cw = m.ContextLength
		}
		if cw == 0 {
			cw = m.MaxContextLength
		}
		out = append(out, RemoteModel{ID: m.ID, OwnedBy: m.OwnedBy, ContextWindow: cw})
	}
	return out, nil
}

// ListGroups implements AdminAPI.
func (a *HTTPAdmin) ListGroups(ctx context.Context) ([]RemoteGroup, error) {
	var resp struct {
		Groups []RemoteGroup `json:"groups"`
	}
	if err := a.do(ctx, http.MethodGet, a.WebURL, "/api/groups", nil, &resp); err != nil {
		return nil, err
	}
	if resp.Groups == nil {
		resp.Groups = []RemoteGroup{}
	}
	return resp.Groups, nil
}

// SaveGroup implements AdminAPI.
func (a *HTTPAdmin) SaveGroup(ctx context.Context, g RemoteGroup) error {
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/groups/save", g, nil)
}

// DeleteGroups implements AdminAPI.
func (a *HTTPAdmin) DeleteGroups(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/groups/delete", map[string]any{"ids": ids}, nil)
}

// ListGatewayKeys implements AdminAPI.
func (a *HTTPAdmin) ListGatewayKeys(ctx context.Context) ([]GatewayKey, error) {
	var resp struct {
		Keys []GatewayKey `json:"keys"`
	}
	if err := a.do(ctx, http.MethodGet, a.WebURL, "/api/caller-keys", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Keys, nil
}

// AddGatewayKey implements AdminAPI. secret may be empty to let magpie
// generate one.
func (a *HTTPAdmin) AddGatewayKey(ctx context.Context, name, secret string) (GatewayKey, error) {
	var resp struct {
		Keys   []GatewayKey `json:"keys"`
		Secret string       `json:"secret"`
	}
	body := map[string]any{"name": name}
	if secret != "" {
		body["secret"] = secret
	}
	if err := a.do(ctx, http.MethodPost, a.WebURL, "/api/caller-keys/add-key", body, &resp); err != nil {
		return GatewayKey{}, err
	}
	// The new key is the last one with this name.
	for i := len(resp.Keys) - 1; i >= 0; i-- {
		if resp.Keys[i].Name == name {
			k := resp.Keys[i]
			k.Secret = resp.Secret
			return k, nil
		}
	}
	return GatewayKey{}, fmt.Errorf("magpie did not list the new gateway key %q", name)
}

// SetGatewayKeyOff implements AdminAPI.
func (a *HTTPAdmin) SetGatewayKeyOff(ctx context.Context, id string, off bool) error {
	action := "on-key"
	if off {
		action = "off-key"
	}
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/caller-keys/"+action, map[string]any{"key": id}, nil)
}

// RemoveGatewayKey implements AdminAPI.
func (a *HTTPAdmin) RemoveGatewayKey(ctx context.Context, id string) error {
	return a.do(ctx, http.MethodPost, a.WebURL, "/api/caller-keys/remove-key", map[string]any{"key": id}, nil)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
