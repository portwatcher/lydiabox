package cli

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

func storeTURN(t *testing.T, d *deps) {
	t.Helper()
	ctx := context.Background()
	state, sec, _, err := loadCloudflare(ctx, d.Store, d.Config.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	state.TURNKeyID, sec.TURNKeySecret = "turnkey1", "turnsecret"
	if err := saveCloudflare(ctx, d.Store, d.Config.TenantID, state, sec); err != nil {
		t.Fatal(err)
	}
}

func TestWireRemoteStoredTURN(t *testing.T) {
	d := remoteDeps(t, true)
	storeTURN(t, d)
	c, err := wireRemote(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	w := d.Config.WebRTC
	if w.CloudflareTURNKeyID != "turnkey1" || w.CloudflareTURNAPIToken != "turnsecret" || !w.RelayOnly || !d.StoredTURN {
		t.Fatalf("stored TURN not used: %+v StoredTURN=%v", w, d.StoredTURN)
	}
	if p := webRTCPort(d); p != 0 {
		t.Fatalf("UDP port = %d; want 0 with the stored TURN key", p)
	}
	if prov, err := desktopICE(w, d.Logger); err != nil || prov == nil {
		t.Fatalf("desktop ICE provider: %v %v", prov, err)
	}
	if st := remoteStatus(t, c); st.TURNKeyID != "turnkey1" || !st.TURNInUse {
		t.Fatalf("status = %+v", st)
	}
}

func TestWireRemoteTURNPrecedence(t *testing.T) {
	t.Setenv("LYDIABOX_WEBRTC_UDP_PORT", "")
	cases := []struct {
		name  string
		env   map[string]string
		yaml  string
		check func(t *testing.T, d *deps)
	}{
		{"YAML TURN wins", nil, "webrtc:\n  turn_urls: [\"turn:turn.example.com:3478\"]\n  turn_username: u\n  turn_password: p\n",
			func(t *testing.T, d *deps) {
				if w := d.Config.WebRTC; w.CloudflareTURNKeyID != "" || len(w.TURNURLs) != 1 || d.StoredTURN {
					t.Fatalf("YAML TURN overridden: %+v", w)
				}
			}},
		{"explicit STUN wins", map[string]string{"LYDIABOX_STUN_URLS": "stun:stun.example.com:3478"}, "", func(t *testing.T, d *deps) {
			if d.Config.WebRTC.CloudflareTURNKeyID != "" || d.Config.WebRTC.RelayOnly || d.StoredTURN {
				t.Fatalf("stored key used: %+v", d.Config.WebRTC)
			}
		}},
		{"explicit Cloudflare TURN wins", map[string]string{"LYDIABOX_CLOUDFLARE_TURN_KEY_ID": "envkey", "LYDIABOX_CLOUDFLARE_TURN_API_TOKEN": "envtoken"}, "",
			func(t *testing.T, d *deps) {
				if w := d.Config.WebRTC; w.CloudflareTURNKeyID != "envkey" || w.CloudflareTURNAPIToken != "envtoken" || w.RelayOnly {
					t.Fatalf("env TURN overridden: %+v", w)
				}
			}},
		{"explicit relay-only false wins", map[string]string{"LYDIABOX_WEBRTC_RELAY_ONLY": "false"}, "", func(t *testing.T, d *deps) {
			w := d.Config.WebRTC
			if w.CloudflareTURNKeyID != "turnkey1" || w.RelayOnly || d.StoredTURN {
				t.Fatalf("want stored key without relay-only: %+v StoredTURN=%v", w, d.StoredTURN)
			}
			if p := webRTCPort(d); p != 50000 {
				t.Fatalf("UDP port = %d, want the default forward", p)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pairs []string
			for k, v := range tc.env {
				pairs = append(pairs, k+"="+v)
			}
			cfg, err := config.Load(config.LoadOptions{HomeDir: t.TempDir(), ConfigFile: writeYAML(t, tc.yaml), Environ: func() []string { return pairs }})
			if err != nil {
				t.Fatal(err)
			}
			d := remoteDeps(t, true)
			cfg.DataDir = d.Config.DataDir
			d.Config = cfg
			storeTURN(t, d)
			if _, err := wireRemote(context.Background(), d); err != nil {
				t.Fatal(err)
			}
			tc.check(t, d)
		})
	}

	// Without a stored key nothing changes.
	d := remoteDeps(t, true)
	if _, err := wireRemote(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if d.Config.WebRTC.Configured() || d.StoredTURN {
		t.Fatalf("WebRTC changed without a stored key: %+v", d.Config.WebRTC)
	}
}

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lydiabox.yaml")
	if err := os.WriteFile(p, []byte("log_level: info\n"+content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
