package link

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func handshakePair(t *testing.T, hostSecret, guestSecret []byte) (hostErr, guestErr error) {
	t.Helper()
	guestDone := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			guestDone <- err
			return
		}
		defer c.CloseNow()
		guestDone <- GuestHandshake(r.Context(), c, guestSecret)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hostErr = HostHandshake(ctx, c, hostSecret)
	return hostErr, <-guestDone
}

func TestHandshakeIsMutual(t *testing.T) {
	a := []byte("secret-number-one-0123")
	b := []byte("secret-number-two-0123")
	if h, g := handshakePair(t, a, a); h != nil || g != nil {
		t.Fatalf("same secret: host %v, guest %v", h, g)
	}
	h, g := handshakePair(t, a, b)
	if !errors.Is(h, ErrUnauthorized) {
		t.Fatalf("host accepted a guest with another secret: %v", h)
	}
	if g == nil {
		t.Fatal("guest accepted a host with another secret")
	}
}

func TestParseListenAddr(t *testing.T) {
	a, err := ParseListenAddr("ws://0.0.0.0:7000")
	if err != nil || a.HostPort != "0.0.0.0:7000" || a.Path != "/" {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = ParseListenAddr("ws://:7000/lydiabox/link")
	if err != nil || a.HostPort != ":7000" || a.Path != "/lydiabox/link" {
		t.Fatalf("%+v %v", a, err)
	}
	for _, bad := range []string{"wss://x:1", "ws://host", "ws://h:1/?a=b", "unix:///x"} {
		if _, err := ParseListenAddr(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSecretFromEnvClearsTheEnvironment(t *testing.T) {
	t.Setenv(EnvSecret, "")
	t.Setenv(EnvSecretFile, "")
	if _, err := SecretFromEnv(); err == nil {
		t.Fatal("no secret accepted")
	}
	t.Setenv(EnvSecret, "short")
	if _, err := SecretFromEnv(); err == nil {
		t.Fatal("short secret accepted")
	}
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte("a-secret-from-a-file-0123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvSecretFile, p)
	s, err := SecretFromEnv()
	if err != nil || string(s) != "a-secret-from-a-file-0123" {
		t.Fatalf("%q %v", s, err)
	}
	if _, ok := os.LookupEnv(EnvSecretFile); ok {
		t.Fatal("the secret file variable stays in the environment")
	}
}

func TestDialWaitsForTheHost(t *testing.T) {
	g, err := NewGuest(GuestOptions{Secret: []byte("0123456789abcdef"), DialWait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	start := time.Now()
	if _, err := g.Dial(context.Background(), 9001); !IsNoLink(err) || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("dial without a link: %v after %s", err, time.Since(start))
	}
}
