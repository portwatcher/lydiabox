//go:build unix

package adapters_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
)

// fakeDSH is a dsh on PATH that prints its arguments, DSH_MODEL, and
// DSH_PERMISSION_MODE, one per line.
const fakeDSH = "#!/bin/sh\necho \"args=$*\"\necho \"model=$DSH_MODEL\"\necho \"mode=$DSH_PERMISSION_MODE\"\necho \"base=$OPENAI_BASE_URL\"\n"

// runDSH runs a dsh command line with a fake dsh and the given home.
func runDSH(t *testing.T, home string, cmd []string) map[string]string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "dsh"), []byte(fakeDSH), 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%q: %v\n%s", cmd, err, out)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(l, "=")
		got[k] = v
	}
	return got
}

// TestDSHWrapper runs the dsh launch command with a fake dsh: the session
// model wins over DSH_MODEL of ~/.dsh/lydiabox.env, the overlay
// ~/.dsh/lydiabox.patch.yml comes right after the profile (launcher flags
// precede the app arguments) and only when a model is known, and every
// launch has DSH_PERMISSION_MODE=danger-full-access.
func TestDSHWrapper(t *testing.T) {
	bare := t.TempDir()
	wired := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wired, ".dsh"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := "OPENAI_BASE_URL=http://127.0.0.1:3425\nDSH_MODEL=kimi/kimi-k3\n"
	if err := os.WriteFile(filepath.Join(wired, ".dsh/lydiabox.env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wired, ".dsh/lydiabox.patch.yml"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := filepath.Join(wired, ".dsh/lydiabox.patch.yml")
	cases := []struct {
		name, home string
		opts       harness.LaunchOptions
		args       string
		model      string
	}{
		{"session model", wired, harness.LaunchOptions{Model: "codex/gpt-6.1-sol"}, "tui --patch " + patch, "codex/gpt-6.1-sol"},
		{"bound model", wired, harness.LaunchOptions{ResumeNativeID: "s1"}, "tui --patch " + patch + " --resume s1", "kimi/kimi-k3"},
		{"no model", bare, harness.LaunchOptions{ResumeNativeID: "s1"}, "tui --resume s1", ""},
		{"no overlay", bare, harness.LaunchOptions{Model: "m/x"}, "tui", "m/x"},
	}
	for _, tc := range cases {
		cmd, _ := adapters.DSH{}.LaunchCommand(tc.opts)
		got := runDSH(t, tc.home, cmd)
		if got["args"] != tc.args || got["model"] != tc.model || got["mode"] != "danger-full-access" {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	got := runDSH(t, wired, adapters.DSHCommand("codex/gpt-6.1-sol", "acp"))
	if got["args"] != "acp --patch "+patch || got["model"] != "codex/gpt-6.1-sol" || got["base"] != "http://127.0.0.1:3425" {
		t.Errorf("acp: %v", got)
	}
}
