package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func instaboxEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		"LYDIABOX_STORAGE_MODE": "provider", "LYDIABOX_INSTABOX_API_KEY": "ibk_secret", "LYDIABOX_INSTABOX_SNAPSHOT": "sn_x",
		"LYDIABOX_VM_CPUS": "2", "LYDIABOX_VM_MEM_MB": "4096",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestInstaboxSettings(t *testing.T) {
	cfg, err := load(t, instaboxEnv(nil), "instabox:\n  api_url: https://broker.example.com\n  idle_pause: 10m\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.EffectiveVMDriver(); got != DriverInstabox {
		t.Fatalf("auto in the provider mode with InstaBox settings = %q", got)
	}
	if cfg.Instabox.APIURL != "https://broker.example.com" || cfg.Instabox.IdlePause != 10*time.Minute {
		t.Fatalf("settings %s", cfg.Instabox)
	}
	if s := fmt.Sprintf("%v %+v", cfg.Instabox, cfg.Instabox); strings.Contains(s, "ibk_secret") {
		t.Fatalf("the API key is printed: %s", s)
	}
	// The default idle pause is the 30 minute safety net.
	cfg, err = load(t, instaboxEnv(nil), "")
	if err != nil || cfg.Instabox.IdlePause != DefaultInstaboxIdlePause {
		t.Fatalf("default idle pause %v %v", cfg.Instabox.IdlePause, err)
	}
	// The key is not accepted in the YAML file.
	if _, err := load(t, nil, "instabox:\n  api_key: ibk_x\n"); err == nil {
		t.Fatal("api_key in YAML was accepted")
	}
	// The key can come from a file.
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("ibk_file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := instaboxEnv(map[string]string{"LYDIABOX_INSTABOX_API_KEY_FILE": keyFile})
	delete(env, "LYDIABOX_INSTABOX_API_KEY")
	if cfg, err = load(t, env, ""); err != nil || cfg.Instabox.APIKey != "ibk_file" {
		t.Fatalf("key file %q %v", cfg.Instabox.APIKey, err)
	}
	if _, err := load(t, instaboxEnv(map[string]string{"LYDIABOX_INSTABOX_API_KEY_FILE": keyFile}), ""); err == nil {
		t.Fatal("a key and a key file were accepted together")
	}
}

func TestInstaboxSettingsAreValidated(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LYDIABOX_STORAGE_MODE": "juicefs", "LYDIABOX_VM_DRIVER": "instabox", "LYDIABOX_S3_ENDPOINT": "https://s3.example.com",
			"LYDIABOX_S3_BUCKET": "b", "LYDIABOX_S3_ACCESS_KEY": "a", "LYDIABOX_S3_SECRET_KEY": "s"}, "needs LYDIABOX_STORAGE_MODE=provider"},
		{map[string]string{"LYDIABOX_INSTABOX_API_KEY": ""}, "LYDIABOX_INSTABOX_API_KEY or LYDIABOX_INSTABOX_API_KEY_FILE is required"},
		{map[string]string{"LYDIABOX_INSTABOX_SNAPSHOT": "", "LYDIABOX_VM_DRIVER": "instabox"}, "LYDIABOX_INSTABOX_SNAPSHOT is required"},
		{map[string]string{"LYDIABOX_VM_MEM_MB": "8192"}, "not an InstaBox size"},
		{map[string]string{"LYDIABOX_WEBRTC_UDP_PORT": "50000"}, "gets no inbound UDP"},
		{map[string]string{"LYDIABOX_INSTABOX_IDLE_PAUSE": "90s"}, "5m to 30m"},
		{map[string]string{"LYDIABOX_INSTABOX_API_URL": "ftp://x"}, "LYDIABOX_INSTABOX_API_URL"},
	} {
		cfg, err := load(t, instaboxEnv(tc.env), "")
		if err == nil {
			err = cfg.ValidateServe()
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.env, err, tc.want)
		}
	}
	// UDP port 0 and an idle pause of 0 (off) are accepted.
	cfg, err := load(t, instaboxEnv(map[string]string{"LYDIABOX_WEBRTC_UDP_PORT": "0", "LYDIABOX_INSTABOX_IDLE_PAUSE": "0"}), "")
	if err == nil {
		err = cfg.ValidateServe()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestCellModeAndUnixListener(t *testing.T) {
	env := map[string]string{"LYDIABOX_CELL_MODE": "true", "LYDIABOX_LISTEN_ADDR": "unix:/run/lbx/alice.sock", "LYDIABOX_STORAGE_MODE": "provider"}
	cfg, err := load(t, env, "")
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.ValidateServe()
	for _, want := range []string{"needs LYDIABOX_TENANT_ID", "needs LYDIABOX_AUTH_TOKEN", "needs LYDIABOX_MASTER_KEY"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("cell mode without %q: %v", want, err)
		}
	}
	if cfg.Remote.Tunnel != TunnelOff {
		t.Fatalf("cell mode tunnel %q", cfg.Remote.Tunnel)
	}
	dir := t.TempDir()
	token, key := filepath.Join(dir, "token"), filepath.Join(dir, "key")
	if err := os.WriteFile(token, []byte("cell-token-0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	env["LYDIABOX_TENANT_ID"], env["LYDIABOX_AUTH_TOKEN_FILE"], env["LYDIABOX_MASTER_KEY_FILE"] = "alice", token, key
	if cfg, err = load(t, env, ""); err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatal(err)
	}
	if cfg.AuthToken != "cell-token-0123456789abcdef" || cfg.MasterKey != strings.Repeat("ab", 32) {
		t.Fatal("secrets from files were not read")
	}
	if p, ok := UnixListenPath(cfg.ListenAddr); !ok || p != "/run/lbx/alice.sock" || !IsLoopbackAddr(cfg.ListenAddr) {
		t.Fatalf("unix listener %q %v", p, ok)
	}
	if _, err := load(t, map[string]string{"LYDIABOX_LISTEN_ADDR": "unix:relative.sock"}, ""); err != nil {
		t.Fatal(err)
	} else if cfg, _ := load(t, map[string]string{"LYDIABOX_LISTEN_ADDR": "unix:relative.sock"}, ""); cfg.Validate() == nil {
		t.Fatal("a relative socket path was accepted")
	}
	if p, ok := UnixListenPath("unix:///run/x.sock"); !ok || p != "/run/x.sock" {
		t.Fatalf("unix:/// form %q", p)
	}
}
