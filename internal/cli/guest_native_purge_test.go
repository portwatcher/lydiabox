package cli

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/store"
)

func purgeRig(t *testing.T, native bool) (*config.Config, *store.Store) {
	t.Helper()
	cfg := config.Defaults(t.TempDir())
	cfg.TenantID = "alice"
	cfg.GuestNativeSet = &native
	for _, f := range []string{"home/.codex/auth.json", "logins.json", "caller-keys.json"} {
		p := filepath.Join(cfg.MagpieDir(), f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("refresh-token-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(cfg.MagpieCacheDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.MagpieCacheDir(), "models.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), cfg.DBPath(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, n := range []string{"gateway-key", "web-key", "caller"} {
		if err := st.PutMagpieSecret(context.Background(), "alice", n, "s"); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, st
}

func TestGuestNativeStartRemovesGatewayState(t *testing.T) {
	cfg, st := purgeRig(t, true)
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	if err := purgeGatewayState(context.Background(), cfg, st, log); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{cfg.MagpieDir(), cfg.MagpieCacheDir()} {
		if _, err := os.Lstat(d); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", d, err)
		}
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		t.Errorf("the state database went away: %v", err)
	}
	if n, err := st.DeleteMagpieSecrets(context.Background(), "alice"); err != nil || n != 0 {
		t.Errorf("secret rows left: %d %v", n, err)
	}
	line := out.String()
	if !strings.Contains(line, "directories=2") || !strings.Contains(line, "secret_rows=3") || strings.Contains(line, "auth.json") || strings.Contains(line, cfg.MagpieDir()) {
		t.Errorf("log %q", line)
	}
	out.Reset()
	if err := purgeGatewayState(context.Background(), cfg, st, log); err != nil || out.Len() != 0 {
		t.Errorf("second start: %v, log %q", err, out.String())
	}
}

func TestGuestNativePurgeFollowsNoLink(t *testing.T) {
	cfg, st := purgeRig(t, true)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep.json")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Both paths become links to a directory outside.
	for _, d := range []string{cfg.MagpieDir(), cfg.MagpieCacheDir()} {
		if err := os.RemoveAll(d); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := purgeGatewayState(context.Background(), cfg, st, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("a link was accepted")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("the link target lost its file: %v", err)
	}
	for _, d := range []string{cfg.MagpieDir(), cfg.MagpieCacheDir()} {
		if fi, err := os.Lstat(d); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s changed: %v", d, err)
		}
	}
	// A link above the path (the cache directory) is not followed either.
	if err := os.Remove(cfg.MagpieCacheDir()); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Dir(cfg.MagpieCacheDir())
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "magpie"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cache); err != nil {
		t.Fatal(err)
	}
	_ = purgeGatewayState(context.Background(), cfg, st, slog.New(slog.DiscardHandler))
	if _, err := os.Stat(filepath.Join(outside, "magpie")); err != nil {
		t.Errorf("a directory behind a linked parent was removed: %v", err)
	}
}

func TestGatewayModeKeepsGatewayState(t *testing.T) {
	cfg, st := purgeRig(t, false)
	if err := purgeGatewayState(context.Background(), cfg, st, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.MagpieDir(), "logins.json")); err != nil {
		t.Errorf("gateway data removed: %v", err)
	}
	if _, err := os.Stat(cfg.MagpieCacheDir()); err != nil {
		t.Errorf("gateway cache removed: %v", err)
	}
	if v, err := st.GetMagpieSecret(context.Background(), "alice", "gateway-key"); err != nil || v != "s" {
		t.Errorf("secret %q %v", v, err)
	}
}
