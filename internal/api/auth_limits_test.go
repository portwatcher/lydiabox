//go:build unix

package api_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
)

// Basic credentials can be cached by a browser after a WebDAV challenge, so
// a cross-origin state change with them is refused like one with a cookie.
// WebDAV clients send no Origin and keep working.
func TestBasicCrossOriginRefused(t *testing.T) {
	e := newEnv(t)
	basic := "Basic " + basicCreds("lydiabox", token)
	cross := map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"}
	if resp, body := e.do(t, "POST", "/api/v1/fs/workspace/mkdir", basic, strings.NewReader(`{"path":"x"}`), cross); resp.StatusCode != 403 {
		t.Fatalf("cross-origin Basic POST: %d %s", resp.StatusCode, body)
	}
	if resp, body := e.do(t, "POST", "/api/v1/fs/workspace/mkdir", basic, strings.NewReader(`{"path":"x"}`), nil); resp.StatusCode != 201 {
		t.Fatalf("Basic POST without Origin: %d %s", resp.StatusCode, body)
	}
	bearer := map[string]string{"Origin": "https://evil.example"}
	if resp, body := e.do(t, "POST", "/api/v1/fs/workspace/mkdir", "Bearer "+token, strings.NewReader(`{"path":"y"}`), bearer); resp.StatusCode != 201 {
		t.Fatalf("Bearer POST with Origin: %d %s", resp.StatusCode, body)
	}
}

// Failed token guesses do not lock out a browser session that is already
// valid: its cookie cannot be guessed, so it skips the limiter.
func TestFailuresDoNotLockOutSession(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.do(t, "POST", "/api/v1/auth/login", "-", strings.NewReader(`{"token":"`+token+`"}`), nil)
	var ck string
	for _, c := range resp.Cookies() {
		if c.Name == api.SessionCookie {
			ck = c.Name + "=" + c.Value
		}
	}
	for i := 0; i < 10; i++ {
		e.do(t, "GET", "/api/v1/status", "Bearer nope", nil, nil)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/status", "Bearer "+token, nil, nil); resp.StatusCode != 429 {
		t.Fatalf("Bearer while blocked: %d, want 429", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/api/v1/status", "-", nil, map[string]string{"Cookie": ck}); resp.StatusCode != 200 {
		t.Fatalf("session while blocked: %d, want 200", resp.StatusCode)
	}
}

// On a cell's UNIX socket every peer has the same address, so the limiter
// keys on Cf-Connecting-Ip: one client's failures do not block the others.
func TestUnixSocketLimiterPerClient(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "cell.sock")
	const tok = "tok-0123456789abcdef"
	auth := api.NewAuthenticator(tok)
	s, err := api.NewServer(api.Options{Addr: "unix:" + sock, Auth: auth, TenantID: "alice"},
		api.CoreRoutes{Auth: auth})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	defer s.Shutdown(context.Background())
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer tr.CloseIdleConnections()
	get := func(bearer, ip string) int {
		req, _ := http.NewRequest("GET", "http://cell/api/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		if ip != "" {
			req.Header.Set("Cf-Connecting-Ip", ip)
		}
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 10; i++ {
		get("wrong", "203.0.113.7")
	}
	if code := get(tok, "203.0.113.7"); code != 429 {
		t.Fatalf("blocked client: %d, want 429", code)
	}
	if code := get(tok, "198.51.100.9"); code != 200 {
		t.Fatalf("other client: %d, want 200", code)
	}
	if code := get(tok, ""); code != 200 {
		t.Fatalf("control plane (no client IP): %d, want 200", code)
	}
}
