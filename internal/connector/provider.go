// Package connector is the host-side Zero-Trust connector manager: the OAuth2
// Authorization Code + PKCE state machine, direct token (PAT) binding,
// encrypted credential storage, single-flight token refresh, upstream revoke,
// and the connector audit log (docs/design.md section 4.6, docs/api.md
// section 11).
//
// Tokens live only in the host state database (AES-256-GCM through
// internal/store) and in memory. They are never logged, never returned by
// the HTTP API, and reach the guest only as short-lived answers of the broker
// in internal/connector/broker.
package connector

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Provider IDs.
const (
	GitHub = "github"
	GDrive = "gdrive"
	Notion = "notion"
	Slack  = "slack"
)

// tokenAuthStyle selects how the client authenticates at the token endpoint.
type tokenAuthStyle int

const (
	// authInForm sends client_id and client_secret in the form body.
	authInForm tokenAuthStyle = iota
	// authBasicJSON sends HTTP Basic client credentials and a JSON body (Notion).
	authBasicJSON
)

// Identity is the upstream account behind a token.
type Identity struct {
	// Label is the account name shown in the UI (login, e-mail, workspace).
	Label string
	// Scopes are the scopes the provider reports for the token, if any.
	Scopes []string
}

// Provider describes one upstream service. Endpoint fields are absolute URLs;
// tests point them at fake providers with Rebase.
type Provider struct {
	ID          string
	DisplayName string

	AuthURL   string
	TokenURL  string
	RevokeURL string
	// APIBase is the REST API root used by identity checks and MCP tools.
	APIBase string
	// UploadBase is the media upload root (Google Drive).
	UploadBase string

	DefaultScopes []string
	// ScopeParam is the authorize query parameter that carries scopes.
	ScopeParam string
	// ScopeSep joins scopes in the authorize URL.
	ScopeSep string
	// ExtraAuthParams are added to the authorize URL.
	ExtraAuthParams map[string]string
	tokenAuth       tokenAuthStyle

	// GitHosts are the git remote hosts served by the git credential helper.
	GitHosts []string

	identify   func(ctx context.Context, h *http.Client, p *Provider, token string) (Identity, error)
	parseToken func(body []byte) (tokenResponse, error)
	revoke     func(ctx context.Context, h *http.Client, p *Provider, client OAuthClient, token string) error
}

// SupportsRevoke reports whether disconnect revokes the token upstream.
func (p *Provider) SupportsRevoke() bool { return p.revoke != nil }

// API returns APIBase joined with path (which starts with "/").
func (p *Provider) API(path string) string { return strings.TrimRight(p.APIBase, "/") + path }

// Upload returns UploadBase joined with path (which starts with "/").
func (p *Provider) Upload(path string) string { return strings.TrimRight(p.UploadBase, "/") + path }

// Rebase rewrites every endpoint of p onto base, keeping the original host as
// the first path segment: "https://api.github.com/user" becomes
// "<base>/api.github.com/user". One fake server can then serve every
// provider host. It is meant for tests and for a reverse proxy in front of
// the upstream APIs.
func (p *Provider) Rebase(base string) {
	base = strings.TrimRight(base, "/")
	for _, f := range []*string{&p.AuthURL, &p.TokenURL, &p.RevokeURL, &p.APIBase, &p.UploadBase} {
		if *f == "" {
			continue
		}
		u, err := url.Parse(*f)
		if err != nil {
			continue
		}
		*f = base + "/" + u.Host + u.EscapedPath()
		if u.RawQuery != "" {
			*f += "?" + u.RawQuery
		}
	}
}

// DefaultProviders returns fresh copies of the built-in providers in display
// order.
func DefaultProviders() []Provider {
	return []Provider{githubProvider(), gdriveProvider(), notionProvider(), slackProvider()}
}

// NotionVersion is the Notion-Version header sent on every Notion API call.
const NotionVersion = "2022-06-28"
