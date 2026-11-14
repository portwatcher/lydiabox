package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRemoteAccessDefaults(t *testing.T) {
	cfg, err := load(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Remote
	if r.Tunnel != TunnelAuto || r.ListenAddrSet || r.AccessConfigured() || r.CloudflareAPIToken != "" {
		t.Fatalf("defaults = %+v", r)
	}
	if cfg.CloudflaredDir() != filepath.Join(cfg.DataDir, "state", "cloudflared") {
		t.Fatal(cfg.CloudflaredDir())
	}
}

func TestRemoteAccessFromFileAndEnv(t *testing.T) {
	yaml := "listen_addr: 0.0.0.0:9000\nremote_access:\n  tunnel: off\n  access_team_domain: file.cloudflareaccess.com\n  access_emails: [A@Example.com, \"@family.example\"]\n"
	cfg, err := load(t, nil, yaml)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Remote
	if !r.ListenAddrSet || r.Tunnel != TunnelOff || r.AccessTeamDomain != "file.cloudflareaccess.com" ||
		!slices.Equal(r.AccessEmails, []string{"a@example.com", "@family.example"}) {
		t.Fatalf("file = %+v", r)
	}
	cfg, err = load(t, map[string]string{
		"LYDIABOX_LISTEN_ADDR":          "127.0.0.1:8080",
		"LYDIABOX_CLOUDFLARE_TUNNEL":    "AUTO",
		"LYDIABOX_ACCESS_AUD":           "aud1",
		"LYDIABOX_ACCESS_EMAILS":        "b@example.com, ",
		"CLOUDFLARE_API_TOKEN":          "generic",
		"LYDIABOX_CLOUDFLARE_API_TOKEN": "specific",
		"CLOUDFLARE_ACCOUNT_ID":         "acct",
	}, yaml)
	if err != nil {
		t.Fatal(err)
	}
	r = cfg.Remote
	if r.Tunnel != TunnelAuto || r.AccessAUD != "aud1" || !slices.Equal(r.AccessEmails, []string{"b@example.com"}) ||
		r.AccessTeamDomain != "file.cloudflareaccess.com" || r.CloudflareAPIToken != "specific" || r.CloudflareAccountID != "acct" {
		t.Fatalf("env = %+v", r)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAccessValidate(t *testing.T) {
	for _, env := range []map[string]string{
		{"LYDIABOX_CLOUDFLARE_TUNNEL": "on"},
		{"LYDIABOX_ACCESS_EMAILS": "not-an-email"},
		{"LYDIABOX_ACCESS_TEAM_DOMAIN": "me@team"},
	} {
		cfg, err := load(t, env, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "LYDIABOX_") {
			t.Errorf("%v: want validation error, got %v", env, err)
		}
	}
}

func TestLoopbackHelpers(t *testing.T) {
	if got := LoopbackListenAddr("0.0.0.0:8080"); got != "127.0.0.1:8080" {
		t.Fatal(got)
	}
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:1": true, "localhost:1": true, "0.0.0.0:1": false, ":8080": false, "bad": false} {
		if IsLoopbackAddr(addr) != want {
			t.Errorf("IsLoopbackAddr(%q) != %v", addr, want)
		}
	}
}
