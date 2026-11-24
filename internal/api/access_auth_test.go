package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/cloudflare/accessjwt"
	"github.com/portwatcher/lydiabox/internal/cloudflare/accessjwt/accessjwttest"
)

type accessEnv struct {
	srv    *httptest.Server
	issuer *accessjwttest.Issuer
}

func newAccessEnv(t *testing.T, enable bool) *accessEnv {
	t.Helper()
	j := accessjwttest.NewIssuer(t)
	auth := api.NewAuthenticator(token)
	if enable {
		v, err := accessjwt.New(accessjwt.Config{
			TeamDomain: accessjwttest.TeamDomain, AUD: accessjwttest.AUD,
			Emails: []string{accessjwttest.Email}, CertsURL: j.CertsURL(),
		})
		if err != nil {
			t.Fatal(err)
		}
		auth.EnableAccess(v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	echo := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		api.CoreRoutes{Auth: auth},
		api.RegistrarFunc(func(r api.Router) {
			r.HandleFunc("GET /api/v1/probe", echo)
			r.HandleFunc("POST /api/v1/probe", echo)
		}))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &accessEnv{srv: srv, issuer: j}
}

func (e *accessEnv) do(t *testing.T, method, path string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func (e *accessEnv) jwt(t *testing.T, edit func(map[string]any)) string {
	c := e.issuer.Claims(time.Now())
	if edit != nil {
		edit(c)
	}
	return e.issuer.Sign(t, c)
}

func TestAccessLogin(t *testing.T) {
	e := newAccessEnv(t, true)
	good := e.jwt(t, nil)

	resp, _ := e.do(t, "GET", "/api/v1/probe", map[string]string{"Cf-Access-Jwt-Assertion": good})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("header: status %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "GET", "/api/v1/probe", map[string]string{"Cookie": "CF_Authorization=" + good})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cookie: status %d", resp.StatusCode)
	}
	_, body := e.do(t, "GET", "/api/v1/auth/session", map[string]string{"Cf-Access-Jwt-Assertion": good})
	var st api.SessionState
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if !st.Authenticated || st.Method != api.MethodAccess || st.Email != accessjwttest.Email || !st.Access {
		t.Fatalf("session = %+v", st)
	}

	for name, tok := range map[string]string{
		"expired":     e.jwt(t, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"not allowed": e.jwt(t, func(c map[string]any) { c["email"] = "eve@example.com" }),
		"wrong aud":   e.jwt(t, func(c map[string]any) { c["aud"] = "other" }),
		"garbage":     "abc.def.ghi",
	} {
		resp, _ := e.do(t, "GET", "/api/v1/probe", map[string]string{"Cf-Access-Jwt-Assertion": tok})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
	_, body = e.do(t, "GET", "/api/v1/auth/session", map[string]string{"Cf-Access-Jwt-Assertion": "abc.def.ghi"})
	if !strings.Contains(string(body), `"authenticated":false`) || !strings.Contains(string(body), `"access":true`) ||
		strings.Contains(string(body), "email") {
		t.Fatalf("session without valid token = %s", body)
	}
	// A valid login of an email that is not allowed is reported, so the UI
	// can say who is signed in at Cloudflare; it does not authenticate.
	eve := e.jwt(t, func(c map[string]any) { c["email"] = "Eve@Example.com" })
	_, body = e.do(t, "GET", "/api/v1/auth/session", map[string]string{"Cf-Access-Jwt-Assertion": eve})
	if !strings.Contains(string(body), `"authenticated":false`) || !strings.Contains(string(body), `"email":"eve@example.com"`) {
		t.Fatalf("session of a not-allowed email = %s", body)
	}
}

func TestAccessKeepsAdminToken(t *testing.T) {
	e := newAccessEnv(t, true)
	resp, _ := e.do(t, "GET", "/api/v1/probe", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bearer: status %d", resp.StatusCode)
	}
	// A wrong Authorization header decides on its own, even with a valid
	// Access token (service token clients send both).
	resp, _ = e.do(t, "GET", "/api/v1/probe", map[string]string{
		"Authorization": "Bearer wrong", "Cf-Access-Jwt-Assertion": e.jwt(t, nil)})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: status %d", resp.StatusCode)
	}
	_, body := e.do(t, "GET", "/api/v1/auth/session", map[string]string{"Authorization": "Bearer " + token})
	if !strings.Contains(string(body), `"method":"token"`) {
		t.Fatalf("session = %s", body)
	}
}

func TestAccessSameOriginCheck(t *testing.T) {
	e := newAccessEnv(t, true)
	tok := e.jwt(t, nil)
	host := strings.TrimPrefix(e.srv.URL, "http://")
	resp, _ := e.do(t, "POST", "/api/v1/probe", map[string]string{"Cf-Access-Jwt-Assertion": tok, "Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST: status %d, want 403", resp.StatusCode)
	}
	resp, _ = e.do(t, "POST", "/api/v1/probe", map[string]string{"Cookie": "CF_Authorization=" + tok, "Origin": "http://" + host})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("same-origin POST: status %d", resp.StatusCode)
	}
	resp, _ = e.do(t, "GET", "/api/v1/probe", map[string]string{"Cf-Access-Jwt-Assertion": tok, "Upgrade": "websocket", "Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin WebSocket: status %d, want 403", resp.StatusCode)
	}
}

func TestAccessDisabledIgnoresHeader(t *testing.T) {
	e := newAccessEnv(t, false)
	resp, _ := e.do(t, "GET", "/api/v1/probe", map[string]string{"Cf-Access-Jwt-Assertion": e.jwt(t, nil)})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	_, body := e.do(t, "GET", "/api/v1/auth/session", nil)
	if !strings.Contains(string(body), `"access":false`) {
		t.Fatalf("session = %s", body)
	}
}

func TestAccessLimiterUsesConnectingIP(t *testing.T) {
	e := newAccessEnv(t, true)
	attacker := map[string]string{"Authorization": "Bearer wrong", "Cf-Connecting-Ip": "203.0.113.7"}
	for i := 0; i < 10; i++ {
		e.do(t, "GET", "/api/v1/probe", attacker)
	}
	resp, _ := e.do(t, "GET", "/api/v1/probe", attacker)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attacker: status %d, want 429", resp.StatusCode)
	}
	// Another client behind the same tunnel (same loopback peer) still works.
	resp, _ = e.do(t, "GET", "/api/v1/probe", map[string]string{"Authorization": "Bearer " + token, "Cf-Connecting-Ip": "198.51.100.9"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("other client: status %d", resp.StatusCode)
	}
}
