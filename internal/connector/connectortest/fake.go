// Package connectortest provides a fake of the GitHub, Google, Notion, and
// Slack endpoints used by internal/connector, served by one httptest server.
// Providers() returns the built-in providers rebased onto it. It is meant for
// tests only.
package connectortest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/portwatcher/lydiabox/internal/connector"
)

// Client credentials the fake token endpoints accept.
const (
	ClientID     = "fake-client-id"
	ClientSecret = "fake-client-secret"
)

// TokenInfo describes a token known to the fake.
type TokenInfo struct {
	Provider string
	Label    string
	Revoked  bool
}

type authCode struct {
	provider, challenge, redirect, label string
}

// Fake is the fake upstream.
type Fake struct {
	Server *httptest.Server
	t      testing.TB

	mu           sync.Mutex
	tokens       map[string]*TokenInfo
	refresh      map[string]*TokenInfo
	codes        map[string]authCode
	seq          int
	refreshes    int
	failRefresh  bool
	bodies       map[string][]byte
	files        map[string]DriveFile
	notionBlocks []map[string]any
}

// ExpiresIn is the token lifetime the fake token endpoints report.
const ExpiresIn = 3600

// DriveFile is a file in the fake Drive.
type DriveFile struct {
	Name     string
	MimeType string
	Content  []byte
}

// New starts a fake upstream that is closed when the test ends.
func New(t testing.TB) *Fake {
	f := &Fake{t: t, tokens: map[string]*TokenInfo{}, refresh: map[string]*TokenInfo{}, codes: map[string]authCode{},
		bodies: map[string][]byte{}, files: map[string]DriveFile{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// Providers returns the built-in providers pointed at the fake.
func (f *Fake) Providers() []connector.Provider {
	ps := connector.DefaultProviders()
	for i := range ps {
		ps[i].Rebase(f.Server.URL)
	}
	return ps
}

// RefreshCount returns how many refresh_token grants succeeded.
func (f *Fake) RefreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes
}

// SetFailRefresh makes refresh_token grants fail with invalid_grant.
func (f *Fake) SetFailRefresh(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failRefresh = v
}

// Body returns the last request body received for "METHOD host/path" and
// whether such a request arrived.
func (f *Fake) Body(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bodies[key]
	return b, ok
}

// PutFile adds a file to the fake Drive.
func (f *Fake) PutFile(id string, file DriveFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[id] = file
}

// File returns a file of the fake Drive.
func (f *Fake) File(id string) (DriveFile, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	file, ok := f.files[id]
	return file, ok
}

// NotionBlocks returns the blocks appended to the fixture page.
func (f *Fake) NotionBlocks() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any{}, f.notionBlocks...)
}

// IssueToken creates a valid access token for provider and label (a PAT).
func (f *Fake) IssueToken(provider, label string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.newTokenLocked("pat", provider, label)
}

func (f *Fake) newTokenLocked(kind, provider, label string) string {
	f.seq++
	tok := fmt.Sprintf("%s-%s-%s-%d", provider, kind, label, f.seq)
	f.tokens[tok] = &TokenInfo{Provider: provider, Label: label}
	return tok
}

// Token returns what the fake knows about tok.
func (f *Fake) Token(tok string) (TokenInfo, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ti, ok := f.tokens[tok]
	if !ok {
		return TokenInfo{}, false
	}
	return *ti, true
}

// RevokeToken marks a token revoked upstream.
func (f *Fake) RevokeToken(tok string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ti, ok := f.tokens[tok]; ok {
		ti.Revoked = true
	}
}

// Approve plays the user consenting on the authorize page: it validates the
// authorize URL and returns the callback URL the provider would redirect to.
func (f *Fake) Approve(authorizeURL, label string) (string, error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	provider := map[string]string{"github.com": connector.GitHub, "accounts.google.com": connector.GDrive,
		"api.notion.com": connector.Notion, "slack.com": connector.Slack}[strings.Split(strings.TrimPrefix(u.Path, "/"), "/")[0]]
	if provider == "" {
		return "", fmt.Errorf("unknown authorize host in %s", u.Path)
	}
	if q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("state") == "" || q.Get("response_type") != "code" {
		return "", fmt.Errorf("authorize URL is missing PKCE or client parameters: %s", u.RawQuery)
	}
	f.mu.Lock()
	f.seq++
	code := fmt.Sprintf("code-%d", f.seq)
	f.codes[code] = authCode{provider: provider, challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), label: label}
	f.mu.Unlock()
	cb, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		return "", err
	}
	cq := url.Values{"code": {code}, "state": {q.Get("state")}}
	cb.RawQuery = cq.Encode()
	return cb.String(), nil
}

func (f *Fake) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// bearer returns the token info of a valid bearer token.
func (f *Fake) bearer(r *http.Request, provider string) (*TokenInfo, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ti, found := f.tokens[tok]
	if !found || ti.Revoked || ti.Provider != provider {
		return nil, false
	}
	return ti, true
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	host, rest := parts[0], ""
	if len(parts) == 2 {
		rest = "/" + parts[1]
	}
	key := r.Method + " " + host + rest
	body := readBody(r)
	f.mu.Lock()

	f.bodies[key] = body
	f.mu.Unlock()
	switch host {
	case "github.com", "oauth2.googleapis.com":
		f.tokenEndpoint(w, r, body, map[string]string{"github.com": connector.GitHub, "oauth2.googleapis.com": connector.GDrive}[host], rest)
	case "api.github.com":
		f.github(w, r, rest, body)
	case "www.googleapis.com":
		f.drive(w, r, rest, body)
	case "api.notion.com":
		if rest == "/v1/oauth/token" {
			f.tokenEndpoint(w, r, body, connector.Notion, rest)
			return
		}
		f.notion(w, r, rest, body)
	case "slack.com":
		if rest == "/api/oauth.v2.access" {
			f.tokenEndpoint(w, r, body, connector.Slack, rest)
			return
		}
		f.slack(w, r, rest, body)
	default:
		http.NotFound(w, r)
	}
}

// tokenEndpoint implements the authorization_code (with PKCE) and
// refresh_token grants, plus Google's revoke endpoint.
func (f *Fake) tokenEndpoint(w http.ResponseWriter, r *http.Request, body []byte, provider, path string) {
	form, clientID, clientSecret := url.Values{}, "", ""
	if provider == connector.Notion {
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		for k, v := range m {
			form.Set(k, v)
		}
		clientID, clientSecret, _ = r.BasicAuth()
	} else {
		form, _ = url.ParseQuery(string(body))
		clientID, clientSecret = form.Get("client_id"), form.Get("client_secret")
	}
	if path == "/revoke" {
		f.RevokeToken(form.Get("token"))
		w.WriteHeader(http.StatusOK)
		return
	}
	oauthErr := func(code string) {
		if provider == connector.Slack {
			f.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": code})
			return
		}
		f.writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": "fake " + code})
	}
	if clientID != ClientID || clientSecret != ClientSecret {
		oauthErr("invalid_client")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var label string
	switch form.Get("grant_type") {
	case "authorization_code":
		c, ok := f.codes[form.Get("code")]
		delete(f.codes, form.Get("code"))
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if !ok || c.provider != provider || c.redirect != form.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			oauthErr("invalid_grant")
			return
		}
		label = c.label
	case "refresh_token":
		ti, ok := f.refresh[form.Get("refresh_token")]
		if !ok || f.failRefresh || ti.Provider != provider {
			oauthErr("invalid_grant")
			return
		}
		delete(f.refresh, form.Get("refresh_token"))
		f.refreshes++
		label = ti.Label
	default:
		oauthErr("unsupported_grant_type")
		return
	}
	access := f.newTokenLocked("oauth", provider, label)
	f.seq++
	refresh := fmt.Sprintf("%s-refresh-%d", provider, f.seq)
	f.refresh[refresh] = &TokenInfo{Provider: provider, Label: label}
	tok := map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "bearer", "expires_in": ExpiresIn}
	if provider == connector.Slack {
		f.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "authed_user": tok})
		return
	}
	f.writeJSON(w, http.StatusOK, tok)
}

func readBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return b
}
