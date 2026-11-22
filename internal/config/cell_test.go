package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCellSettingsFromYAMLAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lydiabox.yaml")
	yaml := "tenant_id: alice\nmagpie:\n  addr: 127.0.0.1:4425\n  web_addr: 127.0.0.1:4426\n" +
		"vm:\n  subnet_index: 7\n  tap: lbxalice\n  nft_table: cell_alice\n  webrtc_udp_port: 50007\n"
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{HomeDir: dir, ConfigFile: p, Environ: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TenantID != "alice" || cfg.Magpie.Addr != "127.0.0.1:4425" || cfg.Magpie.WebAddr != "127.0.0.1:4426" ||
		cfg.VM.SubnetIndex != 7 || cfg.VM.Tap != "lbxalice" || cfg.VM.NftTable != "cell_alice" ||
		cfg.VM.WebRTCPort != 50007 || !cfg.VM.WebRTCPortSet {
		t.Fatalf("yaml: %+v %+v %+v", cfg.TenantID, cfg.Magpie, cfg.VM)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(LoadOptions{HomeDir: dir, ConfigFile: p, Environ: func() []string {
		return []string{"LYDIABOX_TENANT_ID=bob", "LYDIABOX_VM_SUBNET_INDEX=auto", "LYDIABOX_MAGPIE_ADDR=127.0.0.1:5425"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TenantID != "bob" || cfg.VM.SubnetIndex != SubnetAuto || cfg.Magpie.Addr != "127.0.0.1:5425" {
		t.Fatalf("env: %s %+v %+v", cfg.TenantID, cfg.VM, cfg.Magpie)
	}
}

func TestCellDefaultsKeepTheSingleUserValues(t *testing.T) {
	cfg := Defaults(t.TempDir())
	if cfg.TenantID != DefaultTenantID || cfg.Magpie.Addr != "127.0.0.1:3425" || cfg.Magpie.WebAddr != "127.0.0.1:3426" ||
		cfg.VM.SubnetIndex != SubnetAuto || cfg.VM.Tap != "" || cfg.VM.NftTable != "" || cfg.VM.WebRTCPortSet {
		t.Fatalf("defaults %+v %+v", cfg.Magpie, cfg.VM)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAddrsOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"127.0.0.1:1", "127.0.0.1:1", true},
		{"0.0.0.0:1", "127.0.0.1:1", true},
		{":1", "10.0.0.1:1", true},
		{"127.0.0.1:1", "127.0.0.2:1", false},
		{"127.0.0.1:1", "127.0.0.1:2", false},
		{"127.0.0.1:0", "127.0.0.1:0", false},
		{"", "127.0.0.1:1", false},
	} {
		if got := AddrsOverlap(c.a, c.b); got != c.want {
			t.Errorf("AddrsOverlap(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestGuestNativeDefaultsToCellMode(t *testing.T) {
	dir := t.TempDir()
	load := func(env ...string) *Config {
		t.Helper()
		cfg, err := Load(LoadOptions{HomeDir: dir, Environ: func() []string { return env }})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	for _, tc := range []struct {
		env  []string
		want bool
	}{
		{nil, false},
		{[]string{"LYDIABOX_CELL_MODE=true"}, true},
		{[]string{"LYDIABOX_CELL_MODE=true", "LYDIABOX_GUEST_NATIVE=false"}, false},
		{[]string{"LYDIABOX_GUEST_NATIVE=true"}, true},
	} {
		if got := load(tc.env...).GuestNative(); got != tc.want {
			t.Errorf("%v: GuestNative = %v, want %v", tc.env, got, tc.want)
		}
	}
	if _, err := Load(LoadOptions{HomeDir: dir, Environ: func() []string { return []string{"LYDIABOX_GUEST_NATIVE=maybe"} }}); err == nil {
		t.Error("a bad LYDIABOX_GUEST_NATIVE loaded")
	}
}
