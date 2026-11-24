package connector_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/connector"
	"github.com/portwatcher/lydiabox/internal/connector/connectortest"
	"github.com/portwatcher/lydiabox/internal/store"
)

const tenantID = "default"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	mgr   *connector.Manager
	st    *store.Store
	fake  *connectortest.Fake
	clock *clock
}

func newHarness(t *testing.T, mutate func(*connector.Options)) *harness {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := connectortest.New(t)
	clk := &clock{t: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)}
	opts := connector.Options{
		Store: st, Providers: fake.Providers(), Now: clk.Now,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		EnvClients: map[string]connector.OAuthClient{connector.GitHub: {ClientID: connectortest.ClientID, ClientSecret: connectortest.ClientSecret}},
	}
	if mutate != nil {
		mutate(&opts)
	}
	mgr, err := connector.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{mgr: mgr, st: st, fake: fake, clock: clk}
}

func (h *harness) auditKinds(t *testing.T) []string {
	t.Helper()
	page, err := h.mgr.Audit(context.Background(), tenantID, "", 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for i := len(page.Entries) - 1; i >= 0; i-- {
		kinds = append(kinds, page.Entries[i].Kind)
	}
	return kinds
}

// connectOAuth runs authorize -> consent -> callback and returns the state.
func (h *harness) connectOAuth(t *testing.T, provider, label, binding string) {
	t.Helper()
	ctx := context.Background()
	res, err := h.mgr.Authorize(ctx, tenantID, provider, connector.AuthorizeRequest{
		RedirectURI: "http://lydia.test/api/v1/connectors/" + provider + "/callback", SessionBinding: binding, ReturnTo: "/connectors?tab=1"})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := h.fake.Approve(res.AuthorizeURL, label)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(cb)
	returnTo, err := h.mgr.Callback(ctx, tenantID, provider, connector.CallbackRequest{
		Code: u.Query().Get("code"), State: u.Query().Get("state"), SessionBinding: binding})
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if returnTo != "/connectors?tab=1" {
		t.Fatalf("returnTo = %q", returnTo)
	}
}

func TestPKCEAuthorizeCallback(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	binding := connector.SessionBinding("cookie-value")

	res, err := h.mgr.Authorize(ctx, tenantID, connector.GitHub, connector.AuthorizeRequest{
		RedirectURI: "http://lydia.test/api/v1/connectors/github/callback", SessionBinding: binding})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(res.AuthorizeURL)
	q := u.Query()
	if !strings.HasSuffix(u.Path, "/github.com/login/oauth/authorize") || q.Get("scope") != "repo read:org workflow" ||
		q.Get("state") != res.State || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		t.Fatalf("authorize URL = %s", res.AuthorizeURL)
	}
	cb, err := h.fake.Approve(res.AuthorizeURL, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	cbu, _ := url.Parse(cb)
	code, state := cbu.Query().Get("code"), cbu.Query().Get("state")

	// A different browser session cannot complete the flow, and the failed
	// attempt consumes the state.
	if _, err := h.mgr.Callback(ctx, tenantID, connector.GitHub, connector.CallbackRequest{Code: code, State: state,
		SessionBinding: connector.SessionBinding("other")}); !errors.Is(err, connector.ErrInvalidState) {
		t.Fatalf("foreign session: %v", err)
	}
	if _, err := h.mgr.Callback(ctx, tenantID, connector.GitHub, connector.CallbackRequest{Code: code, State: state,
		SessionBinding: binding}); !errors.Is(err, connector.ErrInvalidState) {
		t.Fatalf("state must be single use: %v", err)
	}

	h.connectOAuth(t, connector.GitHub, "octocat", binding)
	c, err := h.mgr.Get(ctx, tenantID, connector.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	if !c.OAuthConfigured || c.OAuthSource != connector.SourceEnv || len(c.Accounts) != 1 {
		t.Fatalf("connector = %+v", c)
	}
	a := c.Accounts[0]
	if a.AccountLabel != "octocat" || a.AuthKind != connector.AuthOAuth2 || a.Status != store.AccountOK ||
		!a.ExpiresAt.Equal(h.clock.Now().Add(time.Hour)) || strings.Join(a.Scopes, " ") != "repo read:org workflow" {
		t.Fatalf("account = %+v", a)
	}
	acct, err := h.st.GetConnectorAccount(ctx, tenantID, connector.GitHub, "octocat")
	if err != nil || acct.RefreshToken == "" {
		t.Fatalf("stored account: %+v %v", acct, err)
	}
	if ti, ok := h.fake.Token(acct.AccessToken); !ok || ti.Label != "octocat" {
		t.Fatalf("stored token unknown to provider")
	}
	if got := strings.Join(h.auditKinds(t), ","); got != connector.KindConnected {
		t.Fatalf("audit = %s", got)
	}
}

func TestAuthorizeValidation(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	redirect := "http://lydia.test/api/v1/connectors/github/callback"
	for _, rt := range []string{"//evil.example", "https://evil.example/", "/\\evil", "connectors"} {
		if _, err := h.mgr.Authorize(ctx, tenantID, connector.GitHub, connector.AuthorizeRequest{RedirectURI: redirect, ReturnTo: rt}); !errors.Is(err, connector.ErrInvalidInput) {
			t.Fatalf("returnTo %q: %v", rt, err)
		}
	}
	if _, err := h.mgr.Authorize(ctx, tenantID, connector.Slack, connector.AuthorizeRequest{RedirectURI: redirect}); !errors.Is(err, connector.ErrNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
	if _, err := h.mgr.Authorize(ctx, tenantID, "dropbox", connector.AuthorizeRequest{RedirectURI: redirect}); !errors.Is(err, connector.ErrUnknownProvider) {
		t.Fatalf("unknown: %v", err)
	}

	// Expired state.
	res, err := h.mgr.Authorize(ctx, tenantID, connector.GitHub, connector.AuthorizeRequest{RedirectURI: redirect})
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := h.fake.Approve(res.AuthorizeURL, "octocat")
	cbu, _ := url.Parse(cb)
	h.clock.Add(11 * time.Minute)
	rt, err := h.mgr.Callback(ctx, tenantID, connector.GitHub, connector.CallbackRequest{Code: cbu.Query().Get("code"), State: res.State})
	var ce *connector.CallbackError
	if !errors.As(err, &ce) || ce.Code != "invalid_state" || rt != connector.DefaultReturnTo {
		t.Fatalf("expired state: %q %v", rt, err)
	}

	// The provider's error parameter is passed through as a safe code.
	res, _ = h.mgr.Authorize(ctx, tenantID, connector.GitHub, connector.AuthorizeRequest{RedirectURI: redirect})
	if _, err := h.mgr.Callback(ctx, tenantID, connector.GitHub, connector.CallbackRequest{State: res.State, Error: "access_denied"}); !errors.As(err, &ce) || ce.Code != "access_denied" {
		t.Fatalf("denied: %v", err)
	}
}

func TestClientSources(t *testing.T) {
	h := newHarness(t, func(o *connector.Options) { o.RelayURL = "https://relay.example/" })
	ctx := context.Background()
	c, _ := h.mgr.Get(ctx, tenantID, connector.Notion)
	if c.OAuthSource != connector.SourceRelay {
		t.Fatalf("notion source = %s", c.OAuthSource)
	}
	res, err := h.mgr.Authorize(ctx, tenantID, connector.Notion, connector.AuthorizeRequest{RedirectURI: "https://lydia.test/cb"})
	if err != nil || !strings.HasPrefix(res.AuthorizeURL, "https://relay.example/v1/notion/authorize?") || strings.Contains(res.AuthorizeURL, "client_id") {
		t.Fatalf("relay authorize: %s %v", res.AuthorizeURL, err)
	}
	c, err = h.mgr.SetClient(ctx, tenantID, connector.Notion, connectortest.ClientID, connectortest.ClientSecret)
	if err != nil || c.OAuthSource != connector.SourceStored {
		t.Fatalf("stored: %+v %v", c, err)
	}
	stored, err := h.st.GetConnectorClient(ctx, tenantID, connector.Notion)
	if err != nil || stored.ClientSecret != connectortest.ClientSecret {
		t.Fatalf("stored client: %+v %v", stored, err)
	}
	// Notion authenticates the token request with Basic auth and JSON.
	h.connectOAuth(t, connector.Notion, "Acme Workspace", "")
	// Env clients win over stored clients.
	if c, _ := h.mgr.SetClient(ctx, tenantID, connector.GitHub, "other", "x"); c.OAuthSource != connector.SourceEnv {
		t.Fatalf("env must win: %s", c.OAuthSource)
	}
	if c, _ := h.mgr.SetClient(ctx, tenantID, connector.Notion, "", ""); c.OAuthSource != connector.SourceRelay {
		t.Fatalf("cleared client: %s", c.OAuthSource)
	}
}

func TestBindToken(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	tok := h.fake.IssueToken(connector.GitHub, "octocat")
	c, err := h.mgr.BindToken(ctx, tenantID, connector.GitHub, connector.BindTokenRequest{Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Accounts) != 1 || c.Accounts[0].AccountLabel != "octocat" || c.Accounts[0].AuthKind != connector.AuthPAT ||
		strings.Join(c.Accounts[0].Scopes, ",") != "repo,read:org" || !c.Accounts[0].ExpiresAt.IsZero() {
		t.Fatalf("connector = %+v", c)
	}
	if _, err := h.mgr.BindToken(ctx, tenantID, connector.GitHub, connector.BindTokenRequest{Token: "ghp_invalid"}); !errors.Is(err, connector.ErrTokenRejected) {
		t.Fatalf("invalid token: %v", err)
	}
	if _, err := h.mgr.BindToken(ctx, tenantID, connector.GitHub, connector.BindTokenRequest{Token: "a b"}); !errors.Is(err, connector.ErrInvalidInput) {
		t.Fatalf("malformed token: %v", err)
	}
	for _, p := range []string{connector.GDrive, connector.Notion, connector.Slack} {
		c, err := h.mgr.BindToken(ctx, tenantID, p, connector.BindTokenRequest{Token: h.fake.IssueToken(p, "me"), AccountLabel: "work"})
		if err != nil || len(c.Accounts) != 1 || c.Accounts[0].AccountLabel != "work" {
			t.Fatalf("%s: %+v %v", p, c, err)
		}
	}
	cred, err := h.mgr.IssueToken(ctx, tenantID, connector.GitHub, "", "git-cred", map[string]string{"host": "github.com"})
	if err != nil || cred.AccessToken != tok || cred.Account != "octocat" {
		t.Fatalf("issue: %+v %v", cred, err)
	}
	page, _ := h.mgr.Audit(ctx, tenantID, connector.GitHub, 0, 10)
	if len(page.Entries) != 2 || page.Entries[0].Kind != connector.KindTokenIssued ||
		page.Entries[0].Detail != `{"account":"octocat","consumer":"git-cred","host":"github.com","provider":"github"}` {
		t.Fatalf("audit = %+v", page.Entries)
	}
	assertNoTokens(t, h, tok)
}

func assertNoTokens(t *testing.T, h *harness, tokens ...string) {
	t.Helper()
	page, err := h.mgr.Audit(context.Background(), tenantID, "", 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Entries {
		for _, tok := range tokens {
			if strings.Contains(e.Detail, tok) {
				t.Fatalf("audit entry %d leaks a token: %s", e.ID, e.Detail)
			}
		}
	}
}

func TestRefreshOnExpirySingleFlight(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.connectOAuth(t, connector.GitHub, "octocat", "")
	before, _ := h.st.GetConnectorAccount(ctx, tenantID, connector.GitHub, "octocat")

	// Still valid: no refresh.
	if c, err := h.mgr.Credential(ctx, tenantID, connector.GitHub, ""); err != nil || c.AccessToken != before.AccessToken {
		t.Fatalf("fresh credential: %v", err)
	}
	h.clock.Add(2 * time.Hour)
	var wg sync.WaitGroup
	got := make([]string, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := h.mgr.Credential(ctx, tenantID, connector.GitHub, "octocat")
			got[i], errs[i] = c.AccessToken, err
		}(i)
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil || got[i] != got[0] || got[0] == before.AccessToken {
			t.Fatalf("caller %d: %q %v", i, got[i], errs[i])
		}
	}
	if h.fake.RefreshCount() != 1 {
		t.Fatalf("refreshes = %d, want 1 (single flight)", h.fake.RefreshCount())
	}
	after, _ := h.st.GetConnectorAccount(ctx, tenantID, connector.GitHub, "octocat")
	if after.RefreshToken == before.RefreshToken || !after.ExpiresAt.Equal(h.clock.Now().Add(time.Hour)) {
		t.Fatalf("rotated account = %+v", after)
	}
	if got := strings.Join(h.auditKinds(t), ","); got != "connector.connected,connector.token.refreshed" {
		t.Fatalf("audit = %s", got)
	}

	// A failed refresh marks the account expired and is audited.
	h.fake.SetFailRefresh(true)
	h.clock.Add(2 * time.Hour)
	if _, err := h.mgr.Credential(ctx, tenantID, connector.GitHub, ""); !errors.Is(err, connector.ErrExpired) {
		t.Fatalf("failed refresh: %v", err)
	}
	c, _ := h.mgr.Get(ctx, tenantID, connector.GitHub)
	if c.Accounts[0].Status != store.AccountExpired {
		t.Fatalf("status = %s", c.Accounts[0].Status)
	}
	page, _ := h.mgr.Audit(ctx, tenantID, connector.GitHub, 0, 1)
	if page.Entries[0].Kind != connector.KindRefreshFailed || !strings.Contains(page.Entries[0].Detail, `"error":"invalid_grant"`) ||
		page.NextBefore != page.Entries[0].ID {
		t.Fatalf("audit page = %+v", page)
	}
	assertNoTokens(t, h, before.AccessToken, before.RefreshToken, after.AccessToken, after.RefreshToken)
}

func TestDisconnectRevokes(t *testing.T) {
	h := newHarness(t, func(o *connector.Options) {
		o.EnvClients[connector.Slack] = connector.OAuthClient{ClientID: connectortest.ClientID, ClientSecret: connectortest.ClientSecret}
	})
	ctx := context.Background()
	h.connectOAuth(t, connector.GitHub, "octocat", "")
	h.connectOAuth(t, connector.Slack, "ana", "")
	gh, _ := h.st.GetConnectorAccount(ctx, tenantID, connector.GitHub, "octocat")
	sl, _ := h.st.GetConnectorAccount(ctx, tenantID, connector.Slack, "ana@"+connectortest.SlackTeam)
	if sl == nil {
		t.Fatal("slack account missing")
	}
	if err := h.mgr.Disconnect(ctx, tenantID, connector.GitHub, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Disconnect(ctx, tenantID, connector.Slack, sl.AccountLabel); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{gh.AccessToken, sl.AccessToken} {
		if ti, _ := h.fake.Token(tok); !ti.Revoked {
			t.Fatalf("token %s not revoked upstream", tok[:8])
		}
	}
	notion := h.fake.IssueToken(connector.Notion, "ws")
	if _, err := h.mgr.BindToken(ctx, tenantID, connector.Notion, connector.BindTokenRequest{Token: notion}); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Disconnect(ctx, tenantID, connector.Notion, "ws"); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Disconnect(ctx, tenantID, connector.Notion, "ws"); !errors.Is(err, connector.ErrNotConnected) {
		t.Fatalf("second disconnect: %v", err)
	}
	page, _ := h.mgr.Audit(ctx, tenantID, connector.Notion, 0, 10)
	if page.Entries[0].Kind != connector.KindDisconnected || !strings.Contains(page.Entries[0].Detail, `"revoked":false`) {
		t.Fatalf("notion audit = %+v", page.Entries)
	}
	// With an OAuth client, Notion tokens are revoked through /v1/oauth/revoke.
	if _, err := h.mgr.SetClient(ctx, tenantID, connector.Notion, connectortest.ClientID, connectortest.ClientSecret); err != nil {
		t.Fatal(err)
	}
	notion = h.fake.IssueToken(connector.Notion, "ws2")
	if _, err := h.mgr.BindToken(ctx, tenantID, connector.Notion, connector.BindTokenRequest{Token: notion}); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Disconnect(ctx, tenantID, connector.Notion, "ws2"); err != nil {
		t.Fatal(err)
	}
	if ti, _ := h.fake.Token(notion); !ti.Revoked {
		t.Fatal("notion token not revoked upstream")
	}
	page, _ = h.mgr.Audit(ctx, tenantID, connector.GitHub, 0, 10)
	if page.Entries[0].Kind != connector.KindDisconnected || !strings.Contains(page.Entries[0].Detail, `"revoked":true`) {
		t.Fatalf("github audit = %+v", page.Entries)
	}
	list, _ := h.mgr.List(ctx, tenantID)
	if len(list) != 4 {
		t.Fatalf("list = %d connectors", len(list))
	}
	for _, c := range list {
		if len(c.Accounts) != 0 {
			t.Fatalf("%s still has accounts", c.Provider)
		}
	}
}

func TestGitAccount(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if _, _, err := h.mgr.GitAccount(ctx, tenantID, "github.com", ""); !errors.Is(err, connector.ErrNotConnected) {
		t.Fatalf("no account: %v", err)
	}
	for _, l := range []string{"alpha", "beta"} {
		if _, err := h.mgr.BindToken(ctx, tenantID, connector.GitHub, connector.BindTokenRequest{
			Token: h.fake.IssueToken(connector.GitHub, l), AccountLabel: l}); err != nil {
			t.Fatal(err)
		}
	}
	if p, l, err := h.mgr.GitAccount(ctx, tenantID, "GitHub.com", "beta"); err != nil || p != connector.GitHub || l != "beta" {
		t.Fatalf("named account: %s %s %v", p, l, err)
	}
	if _, l, _ := h.mgr.GitAccount(ctx, tenantID, "github.com", ""); l != "alpha" {
		t.Fatalf("default account = %s", l)
	}
	if _, _, err := h.mgr.GitAccount(ctx, tenantID, "gitlab.com", ""); !errors.Is(err, connector.ErrNotConnected) {
		t.Fatalf("unknown host: %v", err)
	}
	if err := h.mgr.Disconnect(ctx, tenantID, connector.GitHub, ""); !errors.Is(err, connector.ErrAmbiguous) {
		t.Fatalf("ambiguous disconnect: %v", err)
	}
}
