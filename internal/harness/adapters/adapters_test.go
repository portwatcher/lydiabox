package adapters_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
)

type wantSession struct {
	id, title, workspace, model string
	usage                       *harness.Usage
	created, updated            string
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

var cases = []struct {
	adapter  harness.Adapter
	sessions []wantSession
}{
	{
		adapter: adapters.Claude{},
		sessions: []wantSession{{id: adapterstest.ClaudeID, title: "Resumable uploads for the File API", workspace: adapterstest.Workspace,
			model: "claude-sonnet-4-5-20250929", usage: &harness.Usage{InputTokens: 6200 + 6500 + 6850 + 6990, OutputTokens: 80 + 420 + 60 + 40},
			created: "2026-11-10T05:10:00Z", updated: "2026-11-10T05:14:00Z"}},
	},
	{
		adapter: adapters.Codex{},
		sessions: []wantSession{
			{id: adapterstest.CodexID, title: "Fix flaky upload test", workspace: adapterstest.Workspace, model: "deepseek/deepseek-v4-pro",
				usage: &harness.Usage{InputTokens: 182344, OutputTokens: 20311}, created: "2026-11-10T05:10:00.12Z", updated: "2026-11-10T06:29:58Z"},
			{id: adapterstest.CodexLegacyID, title: "Add a README section that explains how to mount WebDAV on macOS.",
				created: "2025-08-14T09:00:00Z", updated: "2025-08-14T09:00:00Z"},
		},
	},
	{
		adapter: adapters.DSH{},
		sessions: []wantSession{{id: adapterstest.DSHID, title: "Add retry with exponential backoff to the S3 metadata upload in internal/storag…",
			workspace: adapterstest.Workspace, model: "deepseek/deepseek-v4-pro", usage: &harness.Usage{InputTokens: 39800, OutputTokens: 960},
			created: "2026-11-09T05:10:00Z", updated: "2026-11-09T05:10:30Z"}},
	},
	{
		adapter:  adapters.Antigravity{},
		sessions: []wantSession{{id: adapterstest.AntigravityID, title: "Dark mode toggle", workspace: adapterstest.Workspace}},
	},
	{
		adapter: adapters.OpenCode{},
		sessions: []wantSession{
			{id: adapterstest.OpenCodeID, title: "Document the PTY WebSocket protocol", workspace: adapterstest.Workspace, model: "kimi/kimi-k3",
				usage: &harness.Usage{InputTokens: 7000, OutputTokens: 800, CostUSD: 0.0123}, created: "2026-11-09T05:20:00Z", updated: "2026-11-09T05:21:35Z"},
			{id: adapterstest.OpenCodeLegacy, title: "Summarize my meeting notes in notes/2025-10-01.md.", workspace: "/workspace/notes", model: "openai/gpt-5.2",
				usage: &harness.Usage{InputTokens: 1200, OutputTokens: 300, CostUSD: 0.004}, created: "2025-10-01T08:00:00Z", updated: "2025-10-01T08:01:00Z"},
		},
	},
}

func TestDiscover(t *testing.T) {
	home := adapterstest.Home(t)
	for _, tc := range cases {
		t.Run(tc.adapter.Name(), func(t *testing.T) {
			got, err := tc.adapter.DiscoverSessions(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.sessions) {
				t.Fatalf("discovered %d sessions, want %d: %+v", len(got), len(tc.sessions), got)
			}
			for i, w := range tc.sessions {
				g := got[i]
				if g.Harness != tc.adapter.Name() || g.NativeID != w.id || g.Title != w.title || g.WorkspaceDir != w.workspace || g.Model != w.model {
					t.Errorf("session %d = %+v\nwant %+v", i, g, w)
				}
				if !reflect.DeepEqual(g.Usage, w.usage) {
					t.Errorf("%s usage = %+v, want %+v", w.id, g.Usage, w.usage)
				}
				if w.created != "" && (!g.CreatedAt.Equal(ts(w.created)) || !g.UpdatedAt.Equal(ts(w.updated))) {
					t.Errorf("%s times = %s .. %s, want %s .. %s", w.id, g.CreatedAt, g.UpdatedAt, w.created, w.updated)
				}
				if len(g.Files) == 0 {
					t.Errorf("%s has no transcript files", w.id)
				}
			}
			if err := tc.adapter.Purge(home, "../../etc/passwd"); err == nil {
				t.Error("path traversal ID accepted")
			}
		})
	}
}

// TestDSHZstd proves the default compressed dsh format is discovered the
// same way.
func TestDSHZstd(t *testing.T) {
	home := adapterstest.Home(t)
	dir := filepath.Join(home, ".dsh/sessions/---workspace-lydiabox--", adapterstest.DSHID)
	plain, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := adapters.DSH{}.DiscoverSessions(home)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := zstd.NewWriter(nil)
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl.zstd"), enc.EncodeAll(plain, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "session.jsonl")); err != nil {
		t.Fatal(err)
	}
	got, err := adapters.DSH{}.DiscoverSessions(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("zstd discovery differs:\n%+v\n%+v", got, want)
	}
}

func TestPurgeFixtureSessions(t *testing.T) {
	home := adapterstest.Home(t)
	ids := map[string]string{harness.ClaudeCode: adapterstest.ClaudeID, harness.Codex: adapterstest.CodexID,
		harness.DSH: adapterstest.DSHID, harness.Antigravity: adapterstest.AntigravityID, harness.OpenCode: adapterstest.OpenCodeID}
	for _, a := range adapters.All() {
		before, _ := a.DiscoverSessions(home)
		if err := a.Purge(home, ids[a.Name()]); err != nil {
			t.Fatalf("%s purge: %v", a.Name(), err)
		}
		after, _ := a.DiscoverSessions(home)
		if len(after) != len(before)-1 {
			t.Errorf("%s: %d sessions before purge, %d after", a.Name(), len(before), len(after))
		}
		for _, s := range after {
			if s.NativeID == ids[a.Name()] {
				t.Errorf("%s: purged session still discovered", a.Name())
			}
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex/session_index.jsonl")); bytes.Contains(b, []byte(adapterstest.CodexID)) {
		t.Error("codex session index still lists the purged session")
	}
}

func TestLaunchCommands(t *testing.T) {
	cases := []struct {
		a       harness.Adapter
		opts    harness.LaunchOptions
		want    string
		fixedID bool
	}{
		{adapters.Claude{}, harness.LaunchOptions{Model: "kimi/kimi-k3"}, "claude --dangerously-skip-permissions --session-id", true},
		{adapters.Claude{}, harness.LaunchOptions{ResumeNativeID: "abc"}, "claude --dangerously-skip-permissions --resume abc", true},
		{adapters.Codex{}, harness.LaunchOptions{ResumeNativeID: "abc", Model: "m/x"}, "codex resume abc --dangerously-bypass-approvals-and-sandbox --model m/x", true},
		{adapters.Codex{}, harness.LaunchOptions{}, "codex --dangerously-bypass-approvals-and-sandbox", false},
		{adapters.DSH{}, harness.LaunchOptions{Model: "deepseek/x"}, "dsh deepseek/x tui", false},
		{adapters.DSH{}, harness.LaunchOptions{ResumeNativeID: "s1"}, "dsh  tui --resume s1", true},
		{adapters.Antigravity{}, harness.LaunchOptions{ResumeNativeID: "c1"}, "agy --dangerously-skip-permissions --conversation c1", true},
		{adapters.OpenCode{}, harness.LaunchOptions{Model: "kimi/kimi-k3"}, "opencode --auto --model lydiabox/kimi/kimi-k3", false},
		{adapters.OpenCode{}, harness.LaunchOptions{ResumeNativeID: "ses_1"}, "opencode --auto --session ses_1", true},
	}
	for _, tc := range cases {
		cmd, id := tc.a.LaunchCommand(tc.opts)
		if !strings.Contains(strings.Join(cmd, " "), tc.want) {
			t.Errorf("%s: %q lacks %q", tc.a.Name(), cmd, tc.want)
		}
		if tc.fixedID != (id != "") {
			t.Errorf("%s: id %q, fixed=%v", tc.a.Name(), id, tc.fixedID)
		}
	}
}

// TestLaunchFullAccess checks that every launch of every harness (new,
// resumed, with a model) turns off the harness's approval prompts and its
// own command sandbox, and never adds a flag that turns them on again. The
// harness sandboxes block network connections, also to 127.0.0.1 (Chrome
// CDP 9222, gateway 3425, MCP 8910/8911); the VM is the security boundary.
func TestLaunchFullAccess(t *testing.T) {
	cases := []struct {
		a         harness.Adapter
		want      []string
		forbidden []string
	}{
		{adapters.Claude{}, []string{"--dangerously-skip-permissions"}, []string{"--permission-mode", "--allow-dangerously-skip-permissions"}},
		{adapters.Codex{}, []string{"--dangerously-bypass-approvals-and-sandbox"},
			[]string{"-s", "--sandbox", "-a", "--ask-for-approval", "--full-auto", "--approve-for-me"}},
		// dsh: the wrapper exports DSH_PERMISSION_MODE (TestDSHWrapper).
		{adapters.Antigravity{}, []string{"--dangerously-skip-permissions"}, []string{"--sandbox", "--mode"}},
		{adapters.OpenCode{}, []string{"--auto"}, nil},
	}
	variants := []harness.LaunchOptions{
		{},
		{Model: "m/x"},
		{ResumeNativeID: "abc"},
		{ResumeNativeID: "abc", Model: "m/x"},
	}
	for _, tc := range cases {
		for _, opts := range variants {
			cmd, _ := tc.a.LaunchCommand(opts)
			args := map[string]bool{}
			for _, arg := range cmd {
				args[arg] = true
			}
			for _, w := range tc.want {
				if !args[w] {
					t.Errorf("%s %+v: %q lacks %q", tc.a.Name(), opts, cmd, w)
				}
			}
			for _, f := range tc.forbidden {
				if args[f] {
					t.Errorf("%s %+v: %q has %q", tc.a.Name(), opts, cmd, f)
				}
			}
		}
	}
}
