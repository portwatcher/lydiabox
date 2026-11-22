package harness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

func newNativeManager(t *testing.T, native bool) (*harness.Manager, *harnesstest.Runner, *fakeWiring) {
	t.Helper()
	home := adapterstest.Home(t)
	ws := filepath.Join(filepath.Dir(filepath.Dir(home)), "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	runner, wiring := &harnesstest.Runner{}, &fakeWiring{}
	mgr, err := harness.NewManager(harness.ManagerOptions{
		Adapters: adapters.All(), Files: localFiles(home, ws), Discovery: localDiscovery(home, ws), Store: &harnesstest.Store{},
		Runner: runner, Waker: &harnesstest.Waker{}, Wiring: wiring, Logger: quiet(), GuestNative: native,
		MCPServers: []harness.MCPServer{{Name: "lydiabox", URL: "http://127.0.0.1:8910/mcp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	return mgr, runner, wiring
}

func TestSignInShellRunsTheNativeSignIn(t *testing.T) {
	mgr, runner, wiring := newNativeManager(t, true)
	ctx := context.Background()
	if _, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, SignIn: harness.Codex, WorkspaceDir: "/home/agent"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.Last().Spec.Command, " "); got != "bash -lc codex login --device-auth; exec bash -l" {
		t.Errorf("command %q", got)
	}
	if len(wiring.wired) != 1 || wiring.wired[0] != harness.Codex {
		t.Errorf("wired %v, want codex before its sign-in", wiring.wired)
	}
	for _, h := range harness.Harnesses {
		if harness.SignInCommands[h] == "" {
			t.Errorf("%s has no sign-in command", h)
		}
	}
	for _, req := range []harness.CreateRequest{
		{Harness: harness.Codex, SignIn: harness.Codex},
		{Harness: harness.Shell, SignIn: "nope"},
	} {
		if _, err := mgr.Create(ctx, tenant, req); !errors.Is(err, harness.ErrBadRequest) {
			t.Errorf("%+v: %v, want ErrBadRequest", req, err)
		}
	}
	// Guest-native launch commands: Claude Code gets the MCP servers.
	if _, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.ClaudeCode, Model: "opus", WorkspaceDir: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(runner.Last().Spec.Command, " "); !strings.Contains(got, "--model opus --mcp-config") {
		t.Errorf("command %q", got)
	}
}

func TestSignInShellNeedsGuestNative(t *testing.T) {
	mgr, _, _ := newNativeManager(t, false)
	_, err := mgr.Create(context.Background(), tenant, harness.CreateRequest{Harness: harness.Shell, SignIn: harness.ClaudeCode})
	if !errors.Is(err, harness.ErrConflict) {
		t.Errorf("err %v, want ErrConflict", err)
	}
}
