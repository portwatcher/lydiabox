package cli

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/bootstrap"
	"github.com/lydiabox/lydiabox/internal/cloudflare"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/store"
)

func remoteDeps(t *testing.T, stored bool) *deps {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	cfg := config.Defaults(base)
	st, err := store.Open(ctx, filepath.Join(base, "state.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	boot, err := bootstrap.New(bootstrap.Options{BinDir: filepath.Join(base, "bin"), Driver: config.DriverDocker,
		LookPath: func(string) (string, error) { return "", http.ErrMissingFile }})
	if err != nil {
		t.Fatal(err)
	}
	if stored {
		state := cloudflare.State{Hostname: "agent.example.com", Emails: []string{"me@example.com"}, TeamDomain: "team.cloudflareaccess.com",
			AUD: "aud1", TunnelName: "lydiabox-agent-example-com", TunnelID: "tun1", OriginURL: "http://127.0.0.1:8080"}
		if err := saveCloudflare(ctx, st, cfg.TenantID, state, cloudflare.Secrets{TunnelToken: "tok"}); err != nil {
			t.Fatal(err)
		}
	}
	return &deps{Config: cfg, Driver: config.DriverDocker, Logger: slog.New(slog.DiscardHandler), Store: st, Bootstrap: boot,
		Auth: api.NewAuthenticator("test-admin-token-0123456789abcdef"), SkipBootstrap: true}
}

func remoteStatus(t *testing.T, c *component) api.RemoteAccessStatus {
	t.Helper()
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: api.NewAuthenticator("test-admin-token-0123456789abcdef"),
		TenantID: "default", Logger: slog.New(slog.DiscardHandler)}, c.Routes...)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/remote-access", nil)
	req.Header.Set("Authorization", "Bearer test-admin-token-0123456789abcdef")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var st api.RemoteAccessStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return st
}

func sessionAccess(t *testing.T, d *deps) bool {
	t.Helper()
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: d.Auth, TenantID: "default", Logger: slog.New(slog.DiscardHandler)},
		api.CoreRoutes{Auth: d.Auth})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/session", nil))
	return strings.Contains(rec.Body.String(), `"access":true`)
}

func TestWireRemoteNotConfigured(t *testing.T) {
	d := remoteDeps(t, false)
	c, err := wireRemote(context.Background(), d)
	if err != nil || c == nil || c.Start != nil {
		t.Fatalf("component %+v %v", c, err)
	}
	if d.Config.ListenAddr != "0.0.0.0:8080" || sessionAccess(t, d) {
		t.Fatalf("listen %s; Access must stay off", d.Config.ListenAddr)
	}
	if st := remoteStatus(t, c); st.Configured || st.Tunnel != nil || st.Provider != "cloudflare" {
		t.Fatalf("status = %+v", st)
	}
}

func TestWireRemoteTunnel(t *testing.T) {
	d := remoteDeps(t, true)
	c, err := wireRemote(context.Background(), d)
	if err != nil || c.Start == nil || c.Stop == nil {
		t.Fatalf("component %+v %v", c, err)
	}
	if d.Config.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("the tunnel must move the default listener to loopback, got %s", d.Config.ListenAddr)
	}
	if !sessionAccess(t, d) {
		t.Fatal("Access login must be on")
	}
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(ctx)
	st := remoteStatus(t, c)
	if !st.Configured || st.Hostname != "agent.example.com" || st.TeamDomain != "team.cloudflareaccess.com" ||
		st.Tunnel == nil || st.Tunnel.State != "error" || !strings.Contains(st.Tunnel.LastError, "cloudflared") {
		t.Fatalf("status = %+v (tunnel %+v)", st, st.Tunnel)
	}
}

func TestWireRemoteExplicitListenAndTunnelOff(t *testing.T) {
	d := remoteDeps(t, true)
	d.Config.Remote.ListenAddrSet = true
	if _, err := wireRemote(context.Background(), d); err != nil || d.Config.ListenAddr != "0.0.0.0:8080" {
		t.Fatalf("an explicit listen address must win: %s %v", d.Config.ListenAddr, err)
	}
	d = remoteDeps(t, true)
	d.Config.Remote.Tunnel = config.TunnelOff
	c, err := wireRemote(context.Background(), d)
	if err != nil || c.Start != nil || d.Config.ListenAddr != "0.0.0.0:8080" || !sessionAccess(t, d) {
		t.Fatalf("tunnel off: %+v %v listen %s", c, err, d.Config.ListenAddr)
	}
}

func TestWireRemoteManualAccess(t *testing.T) {
	d := remoteDeps(t, false)
	d.Config.Remote.AccessTeamDomain = "team.cloudflareaccess.com"
	d.Config.Remote.AccessAUD = "aud1"
	d.Config.Remote.AccessEmails = []string{"me@example.com"}
	c, err := wireRemote(context.Background(), d)
	if err != nil || c.Start != nil || !sessionAccess(t, d) {
		t.Fatalf("manual Access: %+v %v", c, err)
	}
	if st := remoteStatus(t, c); !st.Configured || st.Emails[0] != "me@example.com" || st.Tunnel != nil {
		t.Fatalf("status = %+v", st)
	}

	d = remoteDeps(t, false)
	d.Config.Remote.AccessAUD = "aud1"
	if _, err := wireRemote(context.Background(), d); err == nil || !strings.Contains(err.Error(), "LYDIABOX_ACCESS_TEAM_DOMAIN") {
		t.Fatalf("incomplete settings: %v", err)
	}
}
