package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/store"
)

// refreshSkew refreshes tokens this long before they expire.
const refreshSkew = 60 * time.Second

// BindTokenRequest binds a pasted token (GitHub PAT, Slack user/bot token,
// Notion integration secret, Google access token).
type BindTokenRequest struct {
	Token        string
	AccountLabel string
	Scopes       []string
}

// BindToken validates a token against the provider's identity endpoint and
// stores it encrypted. The label defaults to the provider user name.
func (m *Manager) BindToken(ctx context.Context, tenantID, provider string, req BindTokenRequest) (Connector, error) {
	p, err := m.Provider(provider)
	if err != nil {
		return Connector{}, err
	}
	tok := strings.TrimSpace(req.Token)
	if tok == "" || len(tok) > 8192 || strings.ContainsAny(tok, " \t\r\n\x00") {
		return Connector{}, fmt.Errorf("%w: token is empty or malformed", ErrInvalidInput)
	}
	label := strings.TrimSpace(req.AccountLabel)
	if err := checkLabel(label, true); err != nil {
		return Connector{}, err
	}
	id, err := p.identify(ctx, m.http, p, tok)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 {
			return Connector{}, fmt.Errorf("%w: %v", ErrTokenRejected, err)
		}
		return Connector{}, err
	}
	if label == "" {
		label = id.Label
	}
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes = id.Scopes
	}
	if scopes, err = cleanScopes(scopes, nil); err != nil {
		return Connector{}, err
	}
	lock := m.accountLock(tenantID, provider, label)
	lock.Lock()
	err = m.store.UpsertConnectorAccount(ctx, tenantID, store.ConnectorAccount{Provider: provider, AccountLabel: label,
		AuthKind: AuthPAT, AccessToken: tok, TokenType: "bearer", Scopes: scopes, Status: store.AccountOK})
	lock.Unlock()
	if err != nil {
		return Connector{}, err
	}
	m.audit(ctx, tenantID, KindConnected, map[string]any{"provider": provider, "account": label, "authKind": AuthPAT})
	return m.Get(ctx, tenantID, provider)
}

func checkLabel(label string, allowEmpty bool) error {
	if label == "" && allowEmpty {
		return nil
	}
	if label == "" || len(label) > 200 || strings.ContainsAny(label, "\x00\r\n\t") {
		return fmt.Errorf("%w: account label", ErrInvalidInput)
	}
	return nil
}

// Credential is a usable access token. It must stay in memory.
type Credential struct {
	Provider    string
	Account     string
	AccessToken string
	TokenType   string
	ExpiresAt   time.Time
}

// Credential returns a fresh access token of an account, refreshing it when
// it is about to expire. An empty label selects the default account (the
// first "ok" account in label order). It writes no token.issued entry;
// callers that hand the token out use IssueToken.
func (m *Manager) Credential(ctx context.Context, tenantID, provider, label string) (Credential, error) {
	p, err := m.Provider(provider)
	if err != nil {
		return Credential{}, err
	}
	if label == "" {
		if label, err = m.defaultLabel(ctx, tenantID, provider); err != nil {
			return Credential{}, err
		}
	}
	acct, err := m.store.GetConnectorAccount(ctx, tenantID, provider, label)
	if errors.Is(err, store.ErrNotFound) {
		return Credential{}, fmt.Errorf("%w: %s account %q", ErrNotConnected, provider, label)
	}
	if err != nil {
		return Credential{}, err
	}
	if m.needsRefresh(acct) {
		if acct, err = m.refresh(ctx, tenantID, p, label); err != nil {
			return Credential{}, err
		}
	}
	if acct.Status == store.AccountRevoked {
		return Credential{}, fmt.Errorf("%w: %s account %q was revoked; reconnect it", ErrNotConnected, provider, label)
	}
	return Credential{Provider: provider, Account: label, AccessToken: acct.AccessToken, TokenType: acct.TokenType,
		ExpiresAt: acct.ExpiresAt}, nil
}

// IssueToken is Credential plus a connector.token.issued audit entry naming
// the consumer (for example "git-cred") and extra non-secret detail.
func (m *Manager) IssueToken(ctx context.Context, tenantID, provider, label, consumer string, extra map[string]string) (Credential, error) {
	c, err := m.Credential(ctx, tenantID, provider, label)
	if err != nil {
		return Credential{}, err
	}
	d := map[string]any{"provider": provider, "account": c.Account, "consumer": consumer}
	for k, v := range extra {
		if _, taken := d[k]; !taken {
			d[k] = v
		}
	}
	m.audit(ctx, tenantID, KindTokenIssued, d)
	return c, nil
}

func (m *Manager) defaultLabel(ctx context.Context, tenantID, provider string) (string, error) {
	accts, err := m.store.ListConnectorAccounts(ctx, tenantID, provider)
	if err != nil {
		return "", err
	}
	for _, a := range accts {
		if a.Status == store.AccountOK {
			return a.AccountLabel, nil
		}
	}
	if len(accts) > 0 {
		return accts[0].AccountLabel, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotConnected, provider)
}

// GitAccount selects the provider and account that serve git credentials for
// host. username, when it names an account of that provider, wins.
func (m *Manager) GitAccount(ctx context.Context, tenantID, host, username string) (provider, label string, err error) {
	host = strings.ToLower(strings.TrimSuffix(host, ":443"))
	for _, id := range m.order {
		p := m.providers[id]
		for _, h := range p.GitHosts {
			if h != host {
				continue
			}
			if username != "" && username != "x-access-token" {
				if _, err := m.store.GetConnectorAccount(ctx, tenantID, id, username); err == nil {
					return id, username, nil
				}
			}
			label, err := m.defaultLabel(ctx, tenantID, id)
			if err != nil {
				return "", "", err
			}
			return id, label, nil
		}
	}
	return "", "", fmt.Errorf("%w: no connector serves git host %q", ErrNotConnected, host)
}

// MarkRevoked records that the provider rejected an account's token.
func (m *Manager) MarkRevoked(ctx context.Context, tenantID, provider, label string) {
	if err := m.store.SetConnectorAccountStatus(context.WithoutCancel(ctx), tenantID, provider, label, store.AccountRevoked); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		m.log.Error("mark connector account revoked", "provider", provider, "account", label, "err", err)
	}
}

func (m *Manager) needsRefresh(a *store.ConnectorAccount) bool {
	return !a.ExpiresAt.IsZero() && !m.now().Add(refreshSkew).Before(a.ExpiresAt)
}

func (m *Manager) accountLock(tenantID, provider, label string) *sync.Mutex {
	key := tenantID + "\x00" + provider + "\x00" + label
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	l, ok := m.refreshes[key]
	if !ok {
		l = &sync.Mutex{}
		m.refreshes[key] = l
	}
	return l
}

// refresh renews an expired access token. Concurrent callers for the same
// account share one upstream call: the first holds the account lock and the
// others re-read the renewed token from the store after it.
func (m *Manager) refresh(ctx context.Context, tenantID string, p *Provider, label string) (*store.ConnectorAccount, error) {
	lock := m.accountLock(tenantID, p.ID, label)
	lock.Lock()
	defer lock.Unlock()
	acct, err := m.store.GetConnectorAccount(ctx, tenantID, p.ID, label)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s account %q", ErrNotConnected, p.ID, label)
	}
	if err != nil {
		return nil, err
	}
	if !m.needsRefresh(acct) {
		return acct, nil
	}
	fail := func(code string, cause error) (*store.ConnectorAccount, error) {
		if err := m.store.SetConnectorAccountStatus(context.WithoutCancel(ctx), tenantID, p.ID, label, store.AccountExpired); err != nil {
			m.log.Error("mark connector account expired", "provider", p.ID, "account", label, "err", err)
		}
		m.audit(ctx, tenantID, KindRefreshFailed, map[string]any{"provider": p.ID, "account": label, "error": code})
		m.log.Warn("connector token refresh failed", "provider", p.ID, "account", label, "error", code)
		return nil, fmt.Errorf("%w: %s account %q: %v", ErrExpired, p.ID, label, cause)
	}
	if acct.RefreshToken == "" {
		return fail("no_refresh_token", errors.New("no refresh token is stored"))
	}
	client, source, err := m.client(ctx, tenantID, p.ID)
	if err != nil {
		return nil, err
	}
	if source == SourceNone {
		return fail("not_configured", ErrNotConfigured)
	}
	tr, err := m.exchange(ctx, p, client, source, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {acct.RefreshToken}})
	if err != nil {
		var oe *oauthError
		code := "refresh_error"
		if errors.As(err, &oe) {
			code = errorCode(oe.Code)
		}
		return fail(code, err)
	}
	acct.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		acct.RefreshToken = tr.RefreshToken
	}
	if tr.TokenType != "" {
		acct.TokenType = tr.TokenType
	}
	if len(tr.Scopes) > 0 {
		acct.Scopes = tr.Scopes
	}
	acct.ExpiresAt = m.expiry(tr.ExpiresIn)
	acct.Status = store.AccountOK
	if err := m.store.UpsertConnectorAccount(ctx, tenantID, *acct); err != nil {
		return nil, err
	}
	m.audit(ctx, tenantID, KindRefreshed, map[string]any{"provider": p.ID, "account": label})
	return acct, nil
}
