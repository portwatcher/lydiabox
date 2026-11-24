package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
	"github.com/portwatcher/lydiabox/internal/harness/adapters/adapterstest"
)

// TestGuestEvents reads session events in the guest (provider mode) the
// same way as on the host, wakes the machine only when asked to, and pages.
func TestGuestEvents(t *testing.T) {
	e := newProviderEnv(t)
	ctx := context.Background()
	local, err := adapters.NewEventReader().Events(e.home, harness.Codex, adapterstest.CodexID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := e.disc.Events(ctx, tenant, harness.Codex, adapterstest.CodexID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != len(local) || page.Next != len(local) || len(page.Events) != len(local) {
		t.Fatalf("guest page next %d total %d events %d, local %d", page.Next, page.Total, len(page.Events), len(local))
	}
	for i := range local {
		if page.Events[i].Key != local[i].Key || page.Events[i].Kind != local[i].Kind {
			t.Fatalf("event %d: guest %+v local %+v", i, page.Events[i], local[i])
		}
	}
	rest, err := e.disc.Events(ctx, tenant, harness.Codex, adapterstest.CodexID, 5, false)
	if err != nil || len(rest.Events) != len(local)-5 || rest.Events[0].Key != local[5].Key {
		t.Fatalf("page after 5 = %+v, %v", rest, err)
	}
	if e.guest.Waker.Wakes() != 0 {
		t.Fatal("a poll woke the machine")
	}

	e.running.Store(false)
	if _, err := e.disc.Events(ctx, tenant, harness.Codex, adapterstest.CodexID, 0, false); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("poll of a stopped machine = %v", err)
	}
	if _, err := e.disc.Events(ctx, tenant, harness.Codex, adapterstest.CodexID, 0, true); err != nil {
		t.Fatal(err)
	}
	if e.guest.Waker.Wakes() == 0 {
		t.Fatal("a read with wake did not wake the machine")
	}
	if _, err := e.disc.Events(ctx, tenant, harness.Codex, "missing-session", 0, true); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("unknown session = %v", err)
	}
	if _, err := e.disc.Events(ctx, tenant, harness.Codex, "../x", 0, true); !errors.Is(err, harness.ErrBadRequest) {
		t.Fatalf("bad ID = %v", err)
	}
}

// oldGuest is a guest program without harness.events.
type oldGuest struct{}

func (oldGuest) FSCall(_ context.Context, _, method string, _, out any) error {
	if method != rpc.MethodHarnessTranscript {
		return rpc.Errorf(rpc.CodeNoMethod, "unknown method %s", method)
	}
	t := harness.Transcript{Entries: []harness.TranscriptEntry{
		{Role: harness.RoleUser, Text: "hi"}, {Role: harness.RoleTool, Tool: "Bash", Text: "ls"}, {Role: harness.RoleAssistant, Text: "done"},
		{Role: harness.RoleTool, Tool: "Bash", Text: "lbx browser open https://example.com"},
	}}
	b, _ := json.Marshal(t)
	return json.Unmarshal(b, out)
}

func TestGuestEventsOldGuest(t *testing.T) {
	g := &harness.GuestDiscovery{Guest: oldGuest{}, Logger: quiet()}
	page, err := g.Events(context.Background(), tenant, harness.ClaudeCode, "abc", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 4 || page.Events[1].Tool == nil || page.Events[1].Tool.Kind != harness.ToolShell || page.Events[2].Text != "done" {
		t.Fatalf("events = %+v", page.Events)
	}
	// A shell call of the lbx CLI has the kind of its lbx command.
	if page.Events[3].Tool == nil || page.Events[3].Tool.Kind != harness.ToolBrowser {
		t.Fatalf("events = %+v", page.Events)
	}
}

func TestToolKind(t *testing.T) {
	for name, want := range map[string]string{
		"Bash": harness.ToolShell, "exec_command": harness.ToolShell, "Read": harness.ToolRead, "apply_patch": harness.ToolEdit,
		"MultiEdit": harness.ToolEdit, "Grep": harness.ToolSearch, "WebFetch": harness.ToolWeb, "TodoWrite": harness.ToolPlan,
		"update_plan": harness.ToolPlan, "Task": harness.ToolAgent, "mcp__github__create_issue": harness.ToolMCP,
		"mcp__lydiabox__computer_screenshot": harness.ToolComputer, "browser_navigate": harness.ToolBrowser,
		"lbx_browser_click": harness.ToolBrowser, "mcp__lydiabox__browser_screenshot": harness.ToolBrowser,
		"something": harness.ToolOther, "": harness.ToolOther,
	} {
		if got := harness.ToolKind(name); got != want {
			t.Errorf("ToolKind(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestShellKind(t *testing.T) {
	for cmd, want := range map[string]string{
		"lbx browser open https://example.com": harness.ToolBrowser, "bash -lc lbx computer screenshot": harness.ToolComputer,
		"/usr/local/bin/lbx browser click e1": harness.ToolBrowser, "lbx tools": harness.ToolShell, "go test ./...": harness.ToolShell,
	} {
		if got := harness.ShellKind(cmd); got != want {
			t.Errorf("ShellKind(%q) = %s, want %s", cmd, got, want)
		}
	}
}
