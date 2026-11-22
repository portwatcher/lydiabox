package api_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/api"
)

var ownerKey = bytes.Repeat([]byte{0x31}, 32)

// ownerToken makes an assertion as the control plane does (lydiabox-cloud
// internal/access/owner).
func ownerToken(t *testing.T, key []byte, edit func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	c := map[string]any{"aud": "lydiabox-cell", "tid": "t-alice", "email": "alice@example.com",
		"iat": now.Unix(), "exp": now.Add(30 * time.Second).Unix()}
	if edit != nil {
		edit(c)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("lydiabox owner assertion v1." + payload))
	return "v1." + payload + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func newOwnerServer(t *testing.T, enable bool) *httptest.Server {
	t.Helper()
	auth := api.NewAuthenticator(token)
	if enable {
		auth.EnableOwnerAssertion(ownerKey, "t-alice", "Alice@Example.com", "https://lydiabox.com/auth/logout",
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	echo := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "t-alice",
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
	return srv
}

func ownerDo(t *testing.T, srv *httptest.Server, method, path string, hdr http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, body
}

func TestOwnerAssertionLogin(t *testing.T) {
	srv := newOwnerServer(t, true)
	hdr := http.Header{api.OwnerAssertionHeader: {ownerToken(t, ownerKey, nil)}}
	if resp, body := ownerDo(t, srv, http.MethodGet, "/api/v1/probe", hdr); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("valid assertion: %d %s", resp.StatusCode, body)
	}
	resp, body := ownerDo(t, srv, http.MethodGet, "/api/v1/auth/session", hdr)
	var st api.SessionState
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &st) != nil || !st.Authenticated ||
		st.Method != api.MethodOwner || st.Email != "alice@example.com" || st.LogoutURL != "https://lydiabox.com/auth/logout" {
		t.Fatalf("session: %d %s", resp.StatusCode, body)
	}
	// A cross-site write with the assertion is refused like a cookie.
	cross := http.Header{api.OwnerAssertionHeader: hdr[api.OwnerAssertionHeader], "Origin": {"https://evil.example"}}
	if resp, _ := ownerDo(t, srv, http.MethodPost, "/api/v1/probe", cross); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin write: %d", resp.StatusCode)
	}
}

func TestOwnerAssertionRefusals(t *testing.T) {
	srv := newOwnerServer(t, true)
	cases := map[string]string{
		"expired": ownerToken(t, ownerKey, func(c map[string]any) {
			c["iat"], c["exp"] = time.Now().Add(-time.Minute).Unix(), time.Now().Add(-time.Second).Unix()
		}),
		"wrong cell":     ownerToken(t, ownerKey, func(c map[string]any) { c["tid"] = "t-bob" }),
		"wrong audience": ownerToken(t, ownerKey, func(c map[string]any) { c["aud"] = "lydiabox-dashboard" }),
		"wrong owner":    ownerToken(t, ownerKey, func(c map[string]any) { c["email"] = "bob@example.com" }),
		"other key":      ownerToken(t, bytes.Repeat([]byte{0x32}, 32), nil),
		"too long":       ownerToken(t, ownerKey, func(c map[string]any) { c["exp"] = time.Now().Add(time.Hour).Unix() }),
		"future": ownerToken(t, ownerKey, func(c map[string]any) {
			c["iat"], c["exp"] = time.Now().Add(time.Minute).Unix(), time.Now().Add(80*time.Second).Unix()
		}),
		"malformed": "v1.not-a-token",
		"version":   "v2" + ownerToken(t, ownerKey, nil)[2:],
	}
	good := ownerToken(t, ownerKey, nil)
	tampered := []byte(good)
	tampered[10] ^= 1
	cases["tampered"] = string(tampered)
	for name, tok := range cases {
		hdr := http.Header{api.OwnerAssertionHeader: {tok}}
		if resp, _ := ownerDo(t, srv, http.MethodGet, "/api/v1/probe", hdr); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	two := http.Header{api.OwnerAssertionHeader: {good, good}}
	if resp, _ := ownerDo(t, srv, http.MethodGet, "/api/v1/probe", two); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("two headers: %d", resp.StatusCode)
	}
	// Without the setting, a valid assertion means nothing.
	off := newOwnerServer(t, false)
	if resp, _ := ownerDo(t, off, http.MethodGet, "/api/v1/probe", http.Header{api.OwnerAssertionHeader: {good}}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("owner login off: %d", resp.StatusCode)
	}
}
