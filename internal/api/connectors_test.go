package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/connector"
	"github.com/lydiabox/lydiabox/internal/connector/connectortest"
	"github.com/lydiabox/lydiabox/internal/store"
)

type connEnv struct {
	srv    *httptest.Server
	fake   *connectortest.Fake
	client *http.Client
	cookie *http.Cookie
}

func newConnEnv(t *testing.T) *connEnv {
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := connector.New(connector.Options{Store: st, Providers: fake.Providers(), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	auth := api.NewAuthenticator(token)
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default", Logger: log},
		api.CoreRoutes{Auth: auth, VM: api.StaticVMStatus{State: api.VMStopped, Driver: "docker"}, Storage: fakeStorage{}},
		api.ConnectorRoutes{Manager: mgr, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	e := &connEnv{srv: httptest.NewServer(s.Handler()), fake: fake}
	t.Cleanup(e.srv.Close)
	e.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := http.Post(e.srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"token":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie {
			e.cookie = c
		}
	}
	if e.cookie == nil {
		t.Fatal("no session cookie")
	}
	return e
}

// do sends a request authenticated with the session cookie (cookie=true)
// or the bearer token, and decodes a JSON answer into out.
func (e *connEnv) do(t *testing.T, method, path, body string, cookie bool, out any) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie {
		req.AddCookie(e.cookie)
	} else {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp
}

func TestConnectorRoutes(t *testing.T) {
	e := newConnEnv(t)
	var list struct {
		Connectors []connector.Connector `json:"connectors"`
	}
	if r := e.do(t, "GET", "/api/v1/connectors", "", false, &list); r.StatusCode != 200 || len(list.Connectors) != 4 ||
		list.Connectors[0].Provider != "github" || list.Connectors[0].OAuthSource != "none" ||
		strings.Join(list.Connectors[0].DefaultScopes, " ") != "repo read:org workflow" {
		t.Fatalf("list: %d %+v", r.StatusCode, list)
	}
	if r := e.do(t, "GET", "/api/v1/connectors/dropbox", "", false, nil); r.StatusCode != 404 {
		t.Fatalf("unknown provider: %d", r.StatusCode)
	}
	resp, _ := http.Get(e.srv.URL + "/api/v1/connectors")
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated list: %d", resp.StatusCode)
	}
	resp.Body.Close()
	if r := e.do(t, "POST", "/api/v1/connectors/github/authorize", "", true, nil); r.StatusCode != 409 {
		t.Fatalf("authorize without client: %d", r.StatusCode)
	}

	var c connector.Connector
	if r := e.do(t, "PUT", "/api/v1/connectors/github/client", `{"clientId":"`+connectortest.ClientID+`","clientSecret":"`+connectortest.ClientSecret+`"}`,
		true, &c); r.StatusCode != 200 || c.OAuthSource != "stored" || !c.OAuthConfigured {
		t.Fatalf("put client: %d %+v", r.StatusCode, c)
	}

	var auth struct {
		AuthorizeURL string `json:"authorizeUrl"`
		State        string `json:"state"`
	}
	if r := e.do(t, "POST", "/api/v1/connectors/github/authorize", `{"returnTo":"/connectors?x=1"}`, true, &auth); r.StatusCode != 200 ||
		!strings.Contains(auth.AuthorizeURL, "redirect_uri="+strings.ReplaceAll(strings.ReplaceAll(e.srv.URL, ":", "%3A"), "/", "%2F")+"%2Fapi%2Fv1%2Fconnectors%2Fgithub%2Fcallback") {
		t.Fatalf("authorize: %d %+v", r.StatusCode, auth)
	}
	cb, err := e.fake.Approve(auth.AuthorizeURL, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	// The callback is public but bound to the browser session.
	req, _ := http.NewRequest("GET", cb, nil)
	resp, err = e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/connectors?error=invalid_state&provider=github" {
		t.Fatalf("callback without session: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	e.do(t, "POST", "/api/v1/connectors/github/authorize", `{"returnTo":"/connectors?x=1"}`, true, &auth)
	cb, _ = e.fake.Approve(auth.AuthorizeURL, "octocat")
	req, _ = http.NewRequest("GET", cb, nil)
	req.AddCookie(e.cookie)
	resp, err = e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/connectors?x=1&connected=github" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	tok := e.fake.IssueToken("slack", "ana")
	if r := e.do(t, "POST", "/api/v1/connectors/slack/token", `{"token":"`+tok+`"}`, false, &c); r.StatusCode != 201 ||
		len(c.Accounts) != 1 || c.Accounts[0].AccountLabel != "ana@acme" || c.Accounts[0].AuthKind != "pat" {
		t.Fatalf("token: %d %+v", r.StatusCode, c)
	}
	if r := e.do(t, "POST", "/api/v1/connectors/slack/token", `{"token":"xoxp-bad"}`, false, nil); r.StatusCode != 400 {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	if r := e.do(t, "GET", "/api/v1/connectors/github", "", false, &c); r.StatusCode != 200 || len(c.Accounts) != 1 || c.Accounts[0].AuthKind != "oauth2" {
		t.Fatalf("get: %+v", c)
	}
	if r := e.do(t, "DELETE", "/api/v1/connectors/slack?account=ana@acme", "", false, nil); r.StatusCode != 204 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r := e.do(t, "DELETE", "/api/v1/connectors/slack?account=ana@acme", "", false, nil); r.StatusCode != 404 {
		t.Fatalf("second delete: %d", r.StatusCode)
	}

	var page struct {
		Entries []struct {
			ID     int64  `json:"id"`
			Kind   string `json:"kind"`
			Detail string `json:"detail"`
		} `json:"entries"`
		NextBefore int64 `json:"nextBefore"`
	}
	if r := e.do(t, "GET", "/api/v1/connectors/audit?limit=2", "", false, &page); r.StatusCode != 200 || len(page.Entries) != 2 ||
		page.Entries[0].Kind != "connector.disconnected" || page.NextBefore != page.Entries[1].ID {
		t.Fatalf("audit: %d %+v", r.StatusCode, page)
	}
	page.NextBefore = 0
	if e.do(t, "GET", "/api/v1/connectors/audit?provider=github", "", false, &page); len(page.Entries) != 1 ||
		page.Entries[0].Kind != "connector.connected" || page.NextBefore != 0 {
		t.Fatalf("github audit: %+v", page)
	}
	for _, q := range []string{"limit=0", "limit=1001", "before=x", "provider=dropbox"} {
		if r := e.do(t, "GET", "/api/v1/connectors/audit?"+q, "", false, nil); r.StatusCode != 400 && r.StatusCode != 404 {
			t.Fatalf("audit %s: %d", q, r.StatusCode)
		}
	}
}
