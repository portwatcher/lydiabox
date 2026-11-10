package connector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/store"
)

// Errors returned by Manager. internal/api maps them to HTTP status codes.
var (
	ErrUnknownProvider = errors.New("unknown connector provider")
	ErrNotConnected    = errors.New("connector is not connected")
	ErrNotConfigured   = errors.New("no OAuth client is configured for this provider")
	ErrInvalidState    = errors.New("OAuth state is invalid, expired, or already used")
	ErrInvalidInput    = errors.New("invalid input")
	ErrTokenRejected   = errors.New("the provider rejected the token")
	ErrExpired         = errors.New("the credential expired and could not be refreshed")
	ErrAmbiguous       = errors.New("several accounts are connected; name one")
)

// OAuth client sources reported as Connector.OAuthSource.
const (
	SourceEnv    = "env"
	SourceStored = "stored"
	SourceRelay  = "relay"
	SourceNone   = "none"
)

// Auth kinds of a stored account.
const (
	AuthOAuth2 = "oauth2"
	AuthPAT    = "pat"
)

// OAuthClient is an OAuth application registration.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
}

// Store is the subset of *store.Store used by the manager.
type Store interface {
	UpsertConnectorAccount(ctx context.Context, tenantID string, a store.ConnectorAccount) error
	GetConnectorAccount(ctx context.Context, tenantID, provider, label string) (*store.ConnectorAccount, error)
	ListConnectorAccounts(ctx context.Context, tenantID, provider string) ([]store.ConnectorAccount, error)
	DeleteConnectorAccount(ctx context.Context, tenantID, provider, label string) error
	SetConnectorAccountStatus(ctx context.Context, tenantID, provider, label, status string) error
	PutConnectorClient(ctx context.Context, tenantID string, c store.ConnectorClient) error
	GetConnectorClient(ctx context.Context, tenantID, provider string) (*store.ConnectorClient, error)
	DeleteConnectorClient(ctx context.Context, tenantID, provider string) error
	AppendAudit(ctx context.Context, tenantID, kind, detail string) (int64, error)
	ListConnectorAudit(ctx context.Context, tenantID string, q store.ConnectorAuditQuery) ([]store.AuditEntry, bool, error)
}

// Options configures a Manager.
type Options struct {
	Store Store
	// EnvClients are the OAuth clients from LYDIABOX_<PROVIDER>_CLIENT_ID and
	// _CLIENT_SECRET. They win over clients stored through the API.
	EnvClients map[string]OAuthClient
	// RelayURL is LYDIABOX_OAUTH_RELAY_URL. It is used for providers without
	// an env or stored client.
	RelayURL string
	// HTTPClient performs every outbound call (defaults to NewHTTPClient()).
	HTTPClient *http.Client
	// Providers defaults to DefaultProviders().
	Providers []Provider
	Logger    *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
	// StateTTL bounds the life of an authorize state (default 10 minutes).
	StateTTL time.Duration
}

// Manager is the tenant-aware connector service.
type Manager struct {
	store     Store
	env       map[string]OAuthClient
	relay     string
	http      *http.Client
	order     []string
	providers map[string]*Provider
	log       *slog.Logger
	now       func() time.Time
	stateTTL  time.Duration

	states *stateStore

	refreshMu sync.Mutex
	refreshes map[string]*sync.Mutex
}

// New builds a Manager.
func New(opts Options) (*Manager, error) {
	if opts.Store == nil {
		return nil, errors.New("connector: Store is required")
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = NewHTTPClient()
	}
	if opts.Providers == nil {
		opts.Providers = DefaultProviders()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.StateTTL <= 0 {
		opts.StateTTL = 10 * time.Minute
	}
	m := &Manager{
		store: opts.Store, env: map[string]OAuthClient{}, relay: strings.TrimRight(opts.RelayURL, "/"),
		http: opts.HTTPClient, providers: map[string]*Provider{}, log: opts.Logger.With("component", "connector"),
		now: opts.Now, stateTTL: opts.StateTTL, states: newStateStore(), refreshes: map[string]*sync.Mutex{},
	}
	for k, v := range opts.EnvClients {
		if v.ClientID != "" {
			m.env[k] = v
		}
	}
	for i := range opts.Providers {
		p := opts.Providers[i]
		if p.ID == "" || m.providers[p.ID] != nil {
			return nil, fmt.Errorf("connector: invalid or duplicate provider %q", p.ID)
		}
		m.providers[p.ID] = &p
		m.order = append(m.order, p.ID)
	}
	return m, nil
}

// HTTPClient returns the outbound client used for provider calls.
func (m *Manager) HTTPClient() *http.Client { return m.http }

// Provider returns a provider by ID.
func (m *Manager) Provider(id string) (*Provider, error) {
	p, ok := m.providers[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownProvider, id)
	}
	return p, nil
}

// Account is the API view of a stored credential. It carries no token.
type Account struct {
	AccountLabel string    `json:"accountLabel"`
	AuthKind     string    `json:"authKind"`
	Scopes       []string  `json:"scopes"`
	ExpiresAt    time.Time `json:"expiresAt"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Status       string    `json:"status"`
}

// Connector is the API view of one provider (docs/api.md section 11).
type Connector struct {
	Provider        string    `json:"provider"`
	DisplayName     string    `json:"displayName"`
	OAuthConfigured bool      `json:"oauthConfigured"`
	OAuthSource     string    `json:"oauthSource"`
	SupportsOAuth   bool      `json:"supportsOAuth"`
	SupportsToken   bool      `json:"supportsToken"`
	DefaultScopes   []string  `json:"defaultScopes"`
	Accounts        []Account `json:"accounts"`
}

// List returns every provider with its accounts.
func (m *Manager) List(ctx context.Context, tenantID string) ([]Connector, error) {
	accts, err := m.store.ListConnectorAccounts(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	out := make([]Connector, 0, len(m.order))
	for _, id := range m.order {
		c, err := m.view(ctx, tenantID, m.providers[id], accts)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Get returns one provider with its accounts.
func (m *Manager) Get(ctx context.Context, tenantID, provider string) (Connector, error) {
	p, err := m.Provider(provider)
	if err != nil {
		return Connector{}, err
	}
	accts, err := m.store.ListConnectorAccounts(ctx, tenantID, provider)
	if err != nil {
		return Connector{}, err
	}
	return m.view(ctx, tenantID, p, accts)
}

func (m *Manager) view(ctx context.Context, tenantID string, p *Provider, accts []store.ConnectorAccount) (Connector, error) {
	_, source, err := m.client(ctx, tenantID, p.ID)
	if err != nil {
		return Connector{}, err
	}
	c := Connector{
		Provider: p.ID, DisplayName: p.DisplayName, OAuthConfigured: source != SourceNone, OAuthSource: source,
		SupportsOAuth: true, SupportsToken: true, DefaultScopes: append([]string{}, p.DefaultScopes...),
		Accounts: []Account{},
	}
	for _, a := range accts {
		if a.Provider != p.ID {
			continue
		}
		c.Accounts = append(c.Accounts, Account{AccountLabel: a.AccountLabel, AuthKind: a.AuthKind,
			Scopes: a.Scopes, ExpiresAt: a.ExpiresAt, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, Status: a.Status})
	}
	return c, nil
}

// client resolves the OAuth client of a provider: env, then stored, then
// relay. The returned client is empty for SourceRelay and SourceNone.
func (m *Manager) client(ctx context.Context, tenantID, provider string) (OAuthClient, string, error) {
	if c, ok := m.env[provider]; ok {
		return c, SourceEnv, nil
	}
	sc, err := m.store.GetConnectorClient(ctx, tenantID, provider)
	if err == nil {
		return OAuthClient{ClientID: sc.ClientID, ClientSecret: sc.ClientSecret}, SourceStored, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return OAuthClient{}, "", err
	}
	if m.relay != "" {
		return OAuthClient{}, SourceRelay, nil
	}
	return OAuthClient{}, SourceNone, nil
}

// SetClient stores (encrypted) the OAuth client of a provider. An empty
// clientID removes the stored client. Env clients keep precedence.
func (m *Manager) SetClient(ctx context.Context, tenantID, provider, clientID, clientSecret string) (Connector, error) {
	if _, err := m.Provider(provider); err != nil {
		return Connector{}, err
	}
	clientID, clientSecret = strings.TrimSpace(clientID), strings.TrimSpace(clientSecret)
	if clientID == "" {
		if clientSecret != "" {
			return Connector{}, fmt.Errorf("%w: clientId is required", ErrInvalidInput)
		}
		if err := m.store.DeleteConnectorClient(ctx, tenantID, provider); err != nil && !errors.Is(err, store.ErrNotFound) {
			return Connector{}, err
		}
		return m.Get(ctx, tenantID, provider)
	}
	if len(clientID) > 512 || len(clientSecret) > 4096 || strings.ContainsAny(clientID+clientSecret, "\x00\r\n") {
		return Connector{}, fmt.Errorf("%w: client credentials are malformed", ErrInvalidInput)
	}
	if err := m.store.PutConnectorClient(ctx, tenantID, store.ConnectorClient{Provider: provider, ClientID: clientID, ClientSecret: clientSecret}); err != nil {
		return Connector{}, err
	}
	return m.Get(ctx, tenantID, provider)
}

// Disconnect deletes an account and revokes its token upstream when the
// provider supports it. An empty label selects the only account.
func (m *Manager) Disconnect(ctx context.Context, tenantID, provider, label string) error {
	p, err := m.Provider(provider)
	if err != nil {
		return err
	}
	if label, err = m.resolveLabel(ctx, tenantID, provider, label); err != nil {
		return err
	}
	lock := m.accountLock(tenantID, provider, label)
	lock.Lock()
	defer lock.Unlock()
	acct, err := m.store.GetConnectorAccount(ctx, tenantID, provider, label)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s account %q", ErrNotConnected, provider, label)
	}
	if err != nil {
		return err
	}
	revoked := false
	if p.revoke != nil && acct.AccessToken != "" {
		client, _, cerr := m.client(ctx, tenantID, provider)
		if cerr != nil {
			return cerr
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		rerr := p.revoke(rctx, m.http, p, client, acct.AccessToken)
		cancel()
		if rerr == nil {
			revoked = true
		} else {
			m.log.Warn("upstream revoke failed; deleting the local credential", "provider", provider, "account", label, "err", rerr)
		}
	}
	if err := m.store.DeleteConnectorAccount(ctx, tenantID, provider, label); err != nil {
		return err
	}
	m.audit(ctx, tenantID, KindDisconnected, map[string]any{"provider": provider, "account": label, "revoked": revoked})
	return nil
}

// resolveLabel returns label, or the single account of provider when label
// is empty.
func (m *Manager) resolveLabel(ctx context.Context, tenantID, provider, label string) (string, error) {
	if label != "" {
		return label, nil
	}
	accts, err := m.store.ListConnectorAccounts(ctx, tenantID, provider)
	if err != nil {
		return "", err
	}
	switch len(accts) {
	case 0:
		return "", fmt.Errorf("%w: %s", ErrNotConnected, provider)
	case 1:
		return accts[0].AccountLabel, nil
	}
	return "", fmt.Errorf("%w (%s)", ErrAmbiguous, provider)
}

// AuditPage is one page of the connector audit log.
type AuditPage struct {
	Entries    []store.AuditEntry `json:"entries"`
	NextBefore int64              `json:"nextBefore,omitempty"`
}

// Audit lists connector audit entries newest first. provider may be empty.
func (m *Manager) Audit(ctx context.Context, tenantID, provider string, before int64, limit int) (AuditPage, error) {
	if provider != "" {
		if _, err := m.Provider(provider); err != nil {
			return AuditPage{}, err
		}
	}
	entries, more, err := m.store.ListConnectorAudit(ctx, tenantID, store.ConnectorAuditQuery{Provider: provider, BeforeID: before, Limit: limit})
	if err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Entries: entries}
	if more && len(entries) > 0 {
		page.NextBefore = entries[len(entries)-1].ID
	}
	return page, nil
}
