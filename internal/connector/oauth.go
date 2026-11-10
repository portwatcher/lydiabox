package connector

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/store"
)

// DefaultReturnTo is the UI page the OAuth callback redirects to by default.
const DefaultReturnTo = "/connectors"

// maxPendingStates bounds the in-memory authorize states.
const maxPendingStates = 1024

// pendingAuth is one authorize request waiting for its callback. It lives
// only in memory: the PKCE verifier never reaches the disk.
type pendingAuth struct {
	tenantID    string
	provider    string
	verifier    string
	redirectURI string
	returnTo    string
	scopes      []string
	binding     string
	expires     time.Time
}

type stateStore struct {
	mu      sync.Mutex
	pending map[string]*pendingAuth
}

func newStateStore() *stateStore { return &stateStore{pending: map[string]*pendingAuth{}} }

func (s *stateStore) put(state string, p *pendingAuth, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.pending {
		if now.After(v.expires) {
			delete(s.pending, k)
		}
	}
	if len(s.pending) >= maxPendingStates {
		return errors.New("connector: too many pending authorizations; retry later")
	}
	s.pending[state] = p
	return nil
}

// take removes and returns the pending authorization of state (single use).
func (s *stateStore) take(state string) *pendingAuth {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending[state]
	delete(s.pending, state)
	return p
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("connector: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pkceChallenge returns the S256 code challenge of verifier (RFC 7636).
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SessionBinding derives the value that binds an authorize state to a
// browser session (a hash of the session cookie). It returns "" for "".
func SessionBinding(cookieValue string) string {
	if cookieValue == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("lydiabox-oauth-state-v1\x00" + cookieValue))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthorizeRequest starts an OAuth authorization.
type AuthorizeRequest struct {
	Scopes   []string
	ReturnTo string
	// RedirectURI is <origin>/api/v1/connectors/<provider>/callback.
	RedirectURI string
	// SessionBinding is SessionBinding(cookie) of the requesting browser
	// session; the callback must present the same session. Empty when the
	// request was not cookie-authenticated.
	SessionBinding string
}

// AuthorizeResult is the answer of POST .../authorize.
type AuthorizeResult struct {
	AuthorizeURL string `json:"authorizeUrl"`
	State        string `json:"state"`
}

// Authorize creates a single-use state with a PKCE verifier and returns the
// provider (or relay) authorization URL.
func (m *Manager) Authorize(ctx context.Context, tenantID, provider string, req AuthorizeRequest) (AuthorizeResult, error) {
	p, err := m.Provider(provider)
	if err != nil {
		return AuthorizeResult{}, err
	}
	client, source, err := m.client(ctx, tenantID, provider)
	if err != nil {
		return AuthorizeResult{}, err
	}
	if source == SourceNone {
		return AuthorizeResult{}, fmt.Errorf("%w (%s)", ErrNotConfigured, provider)
	}
	returnTo, err := cleanReturnTo(req.ReturnTo)
	if err != nil {
		return AuthorizeResult{}, err
	}
	scopes, err := cleanScopes(req.Scopes, p.DefaultScopes)
	if err != nil {
		return AuthorizeResult{}, err
	}
	if u, err := url.Parse(req.RedirectURI); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return AuthorizeResult{}, fmt.Errorf("%w: redirect URI %q", ErrInvalidInput, req.RedirectURI)
	}
	state, err := randomToken(32)
	if err != nil {
		return AuthorizeResult{}, err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return AuthorizeResult{}, err
	}
	pa := &pendingAuth{tenantID: tenantID, provider: provider, verifier: verifier, redirectURI: req.RedirectURI,
		returnTo: returnTo, scopes: scopes, binding: req.SessionBinding, expires: m.now().Add(m.stateTTL)}
	if err := m.states.put(state, pa, m.now()); err != nil {
		return AuthorizeResult{}, err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("redirect_uri", req.RedirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	if p.ScopeParam != "" && len(scopes) > 0 {
		q.Set(p.ScopeParam, strings.Join(scopes, p.ScopeSep))
	}
	for k, v := range p.ExtraAuthParams {
		q.Set(k, v)
	}
	base := p.AuthURL
	if source == SourceRelay {
		base = m.relay + "/v1/" + url.PathEscape(provider) + "/authorize"
	} else {
		q.Set("client_id", client.ClientID)
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return AuthorizeResult{AuthorizeURL: base + sep + q.Encode(), State: state}, nil
}

// CallbackRequest carries the query of the OAuth redirect.
type CallbackRequest struct {
	Code  string
	State string
	// Error is the provider's error parameter (for example access_denied).
	Error          string
	SessionBinding string
}

// CallbackError is a failed callback. Code is a short snake_case reason
// that the UI receives as ?error=<code>.
type CallbackError struct {
	Code string
	Err  error
}

func (e *CallbackError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *CallbackError) Unwrap() error { return e.Err }

// Callback completes an authorization: it consumes the state, exchanges the
// code with the PKCE verifier, identifies the account, and stores the
// encrypted tokens. It always returns the path to redirect the browser to.
func (m *Manager) Callback(ctx context.Context, tenantID, provider string, req CallbackRequest) (string, error) {
	var pa *pendingAuth
	if req.State != "" {
		pa = m.states.take(req.State)
	}
	if pa == nil || pa.tenantID != tenantID || pa.provider != provider || m.now().After(pa.expires) ||
		(pa.binding != "" && subtle.ConstantTimeCompare([]byte(pa.binding), []byte(req.SessionBinding)) != 1) {
		return DefaultReturnTo, &CallbackError{Code: "invalid_state", Err: ErrInvalidState}
	}
	if req.Error != "" {
		return pa.returnTo, &CallbackError{Code: errorCode(req.Error), Err: errors.New("authorization was not granted")}
	}
	if req.Code == "" {
		return pa.returnTo, &CallbackError{Code: "invalid_request", Err: fmt.Errorf("%w: missing code", ErrInvalidInput)}
	}
	p, err := m.Provider(provider)
	if err != nil {
		return pa.returnTo, &CallbackError{Code: "unknown_provider", Err: err}
	}
	client, source, err := m.client(ctx, tenantID, provider)
	if err != nil {
		return pa.returnTo, &CallbackError{Code: "server_error", Err: err}
	}
	if source == SourceNone {
		return pa.returnTo, &CallbackError{Code: "not_configured", Err: ErrNotConfigured}
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {req.Code}, "redirect_uri": {pa.redirectURI},
		"code_verifier": {pa.verifier}}
	tr, err := m.exchange(ctx, p, client, source, form)
	if err != nil {
		return pa.returnTo, &CallbackError{Code: "exchange_failed", Err: err}
	}
	id, err := p.identify(ctx, m.http, p, tr.AccessToken)
	if err != nil {
		return pa.returnTo, &CallbackError{Code: "identity_failed", Err: err}
	}
	scopes := tr.Scopes
	if len(scopes) == 0 {
		scopes = pa.scopes
	}
	acct := store.ConnectorAccount{Provider: provider, AccountLabel: id.Label, AuthKind: AuthOAuth2,
		AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, TokenType: tr.TokenType,
		ExpiresAt: m.expiry(tr.ExpiresIn), Scopes: scopes, Status: store.AccountOK}
	if err := m.store.UpsertConnectorAccount(ctx, tenantID, acct); err != nil {
		return pa.returnTo, &CallbackError{Code: "server_error", Err: err}
	}
	m.audit(ctx, tenantID, KindConnected, map[string]any{"provider": provider, "account": id.Label,
		"authKind": AuthOAuth2, "source": source})
	return pa.returnTo, nil
}

func (m *Manager) expiry(expiresIn int64) time.Time {
	if expiresIn <= 0 {
		return time.Time{}
	}
	return m.now().Add(time.Duration(expiresIn) * time.Second).UTC()
}

// exchange posts form to the token endpoint (or the relay) and parses the
// answer. Errors carry only the provider's error code and description.
func (m *Manager) exchange(ctx context.Context, p *Provider, client OAuthClient, source string, form url.Values) (tokenResponse, error) {
	endpoint := p.TokenURL
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	basic := false
	switch {
	case source == SourceRelay:
		endpoint = m.relay + "/v1/" + url.PathEscape(p.ID) + "/token"
		body = strings.NewReader(form.Encode())
	case p.tokenAuth == authBasicJSON:
		obj := map[string]string{}
		for k := range form {
			obj[k] = form.Get(k)
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return tokenResponse{}, err
		}
		body, contentType, basic = bytes.NewReader(b), "application/json", true
	default:
		form.Set("client_id", client.ClientID)
		if client.ClientSecret != "" {
			form.Set("client_secret", client.ClientSecret)
		}
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "lydiabox-connector")
	if basic {
		req.SetBasicAuth(client.ClientID, client.ClientSecret)
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return tokenResponse{}, transportError(p.ID, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, transportError(p.ID, err)
	}
	tr, perr := p.parseToken(raw)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var oe *oauthError
		if errors.As(perr, &oe) && oe.Code != "invalid_response" {
			return tokenResponse{}, oe
		}
		return tokenResponse{}, &APIError{Provider: p.ID, Status: resp.StatusCode, Message: "token endpoint error"}
	}
	return tr, perr
}

func cleanReturnTo(s string) (string, error) {
	if s == "" {
		return DefaultReturnTo, nil
	}
	if len(s) > 2048 || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "/\\") ||
		strings.ContainsAny(s, "\\\x00\r\n\t") {
		return "", fmt.Errorf("%w: returnTo must be a same-origin path", ErrInvalidInput)
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", fmt.Errorf("%w: returnTo must be a same-origin path", ErrInvalidInput)
	}
	return s, nil
}

func cleanScopes(scopes, defaults []string) ([]string, error) {
	if len(scopes) == 0 {
		return append([]string{}, defaults...), nil
	}
	if len(scopes) > 64 {
		return nil, fmt.Errorf("%w: too many scopes", ErrInvalidInput)
	}
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if s == "" || len(s) > 256 || strings.ContainsAny(s, " ,\t\r\n\x00") {
			return nil, fmt.Errorf("%w: scope %q", ErrInvalidInput, s)
		}
		out = append(out, s)
	}
	return out, nil
}

// errorCode reduces a provider error parameter to a safe snake_case code.
func errorCode(s string) string {
	if s == "" || len(s) > 64 {
		return "oauth_error"
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' {
			return "oauth_error"
		}
	}
	return s
}
