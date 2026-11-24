package magpie_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/magpie"
	"github.com/portwatcher/lydiabox/internal/magpie/magpietest"
)

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// TestSubscriptionStoreStaysOnHost checks where magpie keeps subscription
// sign-ins: logins.json under XDG_CONFIG_HOME/magpie and the vendor CLI
// files (~/.claude/.credentials.json, ~/.codex/auth.json) under its HOME.
// Every one of these directories must be on the host, outside the JuiceFS
// mount that holds the guest /home/agent.
func TestSubscriptionStoreStaysOnHost(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	runner := &fakeRunner{}
	sup, err := magpie.NewSupervisor(magpie.Options{
		TenantID: "default", Binary: "/opt/magpie", DataDir: cfg.MagpieDir(), Runner: runner,
		Secrets:  &memSecrets{m: map[string]string{}},
		NewAdmin: func(string, string, string) magpie.AdminAPI { return magpietest.NewFakeAdmin() },
		Logger:   quietLogger(), HealthInterval: 10 * time.Millisecond, StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(context.Background())
	waitFor(t, "magpie start", func() bool { runner.mu.Lock(); defer runner.mu.Unlock(); return len(runner.cmds) > 0 })
	runner.mu.Lock()
	cmd := runner.cmds[0]
	runner.mu.Unlock()

	seen := map[string]bool{}
	for _, kv := range cmd.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME":
			seen[k] = true
			if !filepath.IsAbs(v) || !within(v, cfg.DataDir) {
				t.Errorf("%s=%s is not inside the host data directory %s", k, v, cfg.DataDir)
			}
			if within(v, cfg.MountDir()) || within(cfg.HomeDir(), v) {
				t.Errorf("%s=%s overlaps the guest file system %s", k, v, cfg.MountDir())
			}
		}
	}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if !seen[k] {
			t.Errorf("magpie must run with its own %s; without it, it reads the host user's directories", k)
		}
	}
	if !within(cmd.Dir, cfg.StateDir()) {
		t.Errorf("magpie runs in %s, outside the host state directory", cmd.Dir)
	}
}
