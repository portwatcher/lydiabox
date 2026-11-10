package adapters_test

import (
	"bytes"
	"errors"
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

type wantTraj struct {
	goal     string
	modified []string
	actions  []string
	messages []harness.Message
	issues   []string
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
	traj     map[string]wantTraj
}{
	{
		adapter: adapters.Claude{},
		sessions: []wantSession{{id: adapterstest.ClaudeID, title: "Resumable uploads for the File API", workspace: adapterstest.Workspace,
			model: "claude-sonnet-4-5-20250929", usage: &harness.Usage{InputTokens: 6200 + 6500 + 6850 + 6990, OutputTokens: 80 + 420 + 60 + 40},
			created: "2026-11-10T05:10:00Z", updated: "2026-11-10T05:14:00Z"}},
		traj: map[string]wantTraj{adapterstest.ClaudeID: {
			goal:     "Make uploads in the File Manager resumable. Use the Content-Range header and keep partial files in .upload/.",
			modified: []string{"internal/storage/files/http.go"},
			actions:  []string{"read internal/storage/files/http.go", "edit internal/storage/files/http.go", "run go test ./internal/storage/..."},
			messages: []harness.Message{
				{Role: "user", Text: "Make uploads in the File Manager resumable. Use the Content-Range header and keep partial files in .upload/."},
				{Role: "assistant", Text: "I'll look at the upload handler first."},
				{Role: "assistant", Text: "The resume test fails because the handler returns 200 instead of 308. I will fix the status code next."},
				{Role: "user", Text: "Also keep the upload size limit at 10 GiB."},
			},
			issues: []string{"run go test ./internal/storage/... failed: --- FAIL: TestUploadResume (0.01s)",
				"Unanswered request: Also keep the upload size limit at 10 GiB."},
		}},
	},
	{
		adapter: adapters.Codex{},
		sessions: []wantSession{
			{id: adapterstest.CodexID, title: "Fix flaky upload test", workspace: adapterstest.Workspace, model: "deepseek/deepseek-v4-pro",
				usage: &harness.Usage{InputTokens: 182344, OutputTokens: 20311}, created: "2026-11-10T05:10:00.12Z", updated: "2026-11-10T06:29:58Z"},
			{id: adapterstest.CodexLegacyID, title: "Add a README section that explains how to mount WebDAV on macOS.",
				created: "2025-08-14T09:00:00Z", updated: "2025-08-14T09:00:00Z"},
		},
		traj: map[string]wantTraj{
			adapterstest.CodexID: {
				goal:     "Fix the flaky upload test in internal/storage/files. It fails about one run in five.",
				modified: []string{"internal/storage/files/service_test.go"},
				actions: []string{"run go test ./internal/storage/files/ -run TestUpload -count=5", "edit internal/storage/files/service_test.go",
					"run go test ./internal/storage/files/ -run TestUpload -count=5"},
				messages: []harness.Message{
					{Role: "user", Text: "Fix the flaky upload test in internal/storage/files. It fails about one run in five."},
					{Role: "assistant", Text: "The test now passes five times in a row. The race was a shared temp dir that another test removed."},
				},
				issues: []string{},
			},
			adapterstest.CodexLegacyID: {
				goal:     "Add a README section that explains how to mount WebDAV on macOS.",
				modified: []string{},
				actions:  []string{"run grep -n WebDAV README.md"},
				messages: []harness.Message{
					{Role: "user", Text: "Add a README section that explains how to mount WebDAV on macOS."},
					{Role: "assistant", Text: "Added a WebDAV section with the Finder steps."},
				},
				issues: []string{},
			},
		},
	},
	{
		adapter: adapters.DSH{},
		sessions: []wantSession{{id: adapterstest.DSHID, title: "Add retry with exponential backoff to the S3 metadata upload in internal/storag…",
			workspace: adapterstest.Workspace, model: "deepseek/deepseek-v4-pro", usage: &harness.Usage{InputTokens: 39800, OutputTokens: 960},
			created: "2026-11-09T05:10:00Z", updated: "2026-11-09T05:10:30Z"}},
		traj: map[string]wantTraj{adapterstest.DSHID: {
			goal:     "Add retry with exponential backoff to the S3 metadata upload in internal/storage/metasync.go.",
			modified: []string{"internal/storage/metasync.go"},
			actions:  []string{"read internal/storage/metasync.go", "edit internal/storage/metasync.go", "run go vet ./internal/storage/"},
			messages: []harness.Message{
				{Role: "user", Text: "Add retry with exponential backoff to the S3 metadata upload in internal/storage/metasync.go."},
				{Role: "assistant", Text: "I will read metasync.go first."},
				{Role: "assistant", Text: "go vet reports unreachable code at line 58. I will remove the dead return after the retry call."},
			},
			issues: []string{"run go vet ./internal/storage/ failed: internal/storage/metasync.go:58:2: unreachable code"},
		}},
	},
	{
		adapter:  adapters.Antigravity{},
		sessions: []wantSession{{id: adapterstest.AntigravityID, title: "Dark mode toggle", workspace: adapterstest.Workspace}},
		traj: map[string]wantTraj{adapterstest.AntigravityID: {
			goal:     "Add a dark mode toggle to the web UI header. Persist the choice in localStorage.",
			modified: []string{"web/src/components/ThemeToggle.tsx"},
			actions:  []string{"read web/src/components/Header.tsx", "edit web/src/components/ThemeToggle.tsx", "run npm run build"},
			messages: []harness.Message{
				{Role: "user", Text: "Add a dark mode toggle to the web UI header. Persist the choice in localStorage."},
				{Role: "assistant", Text: "I will add a ThemeToggle component and wire it into the header."},
				{Role: "assistant", Text: "The build fails because theme.ts does not exist yet. I will create it next."},
			},
			issues: []string{"run npm run build failed"},
		}},
	},
	{
		adapter: adapters.OpenCode{},
		sessions: []wantSession{
			{id: adapterstest.OpenCodeID, title: "Document the PTY WebSocket protocol", workspace: adapterstest.Workspace, model: "kimi/kimi-k3",
				usage: &harness.Usage{InputTokens: 7000, OutputTokens: 800, CostUSD: 0.0123}, created: "2026-11-09T05:20:00Z", updated: "2026-11-09T05:21:35Z"},
			{id: adapterstest.OpenCodeLegacy, title: "Summarize my meeting notes in notes/2025-10-01.md.", workspace: "/workspace/notes", model: "openai/gpt-5.2",
				usage: &harness.Usage{InputTokens: 1200, OutputTokens: 300, CostUSD: 0.004}, created: "2025-10-01T08:00:00Z", updated: "2025-10-01T08:01:00Z"},
		},
		traj: map[string]wantTraj{adapterstest.OpenCodeID: {
			goal:     "Write docs/pty.md that documents the PTY WebSocket frames: binary data, JSON control, replay.",
			modified: []string{"docs/pty.md"},
			actions:  []string{"edit docs/pty.md", "run markdownlint docs/pty.md"},
			messages: []harness.Message{
				{Role: "user", Text: "Write docs/pty.md that documents the PTY WebSocket frames: binary data, JSON control, replay."},
				{Role: "assistant", Text: "I'll draft the document from section 8 of docs/api.md."},
				{Role: "assistant", Text: "docs/pty.md is written. markdownlint is not installed, so the lint step did not run."},
			},
			issues: []string{"run markdownlint docs/pty.md failed: /bin/sh: markdownlint: command not found"},
		}},
	},
}

func TestDiscoverAndParse(t *testing.T) {
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
			for id, w := range tc.traj {
				tr, err := tc.adapter.ParseTranscript(home, id)
				if err != nil {
					t.Fatal(err)
				}
				checkTraj(t, tr, w)
			}
			if _, err := tc.adapter.ParseTranscript(home, "missing-session"); !errors.Is(err, harness.ErrNotFound) {
				t.Errorf("missing session: %v", err)
			}
			if _, err := tc.adapter.ParseTranscript(home, "../../etc/passwd"); err == nil {
				t.Error("path traversal ID accepted")
			}
		})
	}
}

func checkTraj(t *testing.T, tr *harness.CanonicalTrajectory, w wantTraj) {
	t.Helper()
	if tr.OriginalGoal != w.goal {
		t.Errorf("goal = %q, want %q", tr.OriginalGoal, w.goal)
	}
	if !reflect.DeepEqual(tr.ModifiedFiles, w.modified) {
		t.Errorf("modified = %q, want %q", tr.ModifiedFiles, w.modified)
	}
	if !reflect.DeepEqual(tr.ToolActionsSummary, w.actions) {
		t.Errorf("actions = %q, want %q", tr.ToolActionsSummary, w.actions)
	}
	if !reflect.DeepEqual(tr.RecentMessages, w.messages) {
		t.Errorf("messages = %q, want %q", tr.RecentMessages, w.messages)
	}
	if !reflect.DeepEqual(tr.UnresolvedIssues, w.issues) {
		t.Errorf("issues = %q, want %q", tr.UnresolvedIssues, w.issues)
	}
}

// TestDSHZstd proves the default compressed dsh format parses the same.
func TestDSHZstd(t *testing.T) {
	home := adapterstest.Home(t)
	dir := filepath.Join(home, ".dsh/sessions/---workspace-lydiabox--", adapterstest.DSHID)
	plain, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := adapters.DSH{}.ParseTranscript(home, adapterstest.DSHID)
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
	got, err := adapters.DSH{}.ParseTranscript(home, adapterstest.DSHID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("zstd parse differs:\n%+v\n%+v", got, want)
	}
}

// TestSynthesizeHandoffSeeds checks that every native seed is discoverable
// by its own adapter and carries the handoff.
func TestSynthesizeHandoffSeeds(t *testing.T) {
	traj := &harness.CanonicalTrajectory{OriginalGoal: "Ship the bridge", HandoffMarkdown: "# Handoff\n\n## Original goal\nShip the bridge\n"}
	for _, a := range adapters.All() {
		t.Run(a.Name(), func(t *testing.T) {
			home := t.TempDir()
			cmd, id, err := a.SynthesizeHandoff(home, "/workspace/proj", traj)
			if err != nil {
				t.Fatal(err)
			}
			if len(cmd) == 0 {
				t.Fatal("empty resume command")
			}
			joined := strings.Join(cmd, " ")
			if !strings.Contains(joined, adapters.ContinuePrompt) && a.Name() != harness.DSH {
				t.Errorf("command lacks the continuation prompt: %q", cmd)
			}
			if a.Name() == harness.Antigravity {
				if id != "" {
					t.Errorf("antigravity returned a native ID %q", id)
				}
				return
			}
			if id == "" || !strings.Contains(joined, id) {
				t.Fatalf("id %q not in command %q", id, cmd)
			}
			if a.Name() == harness.OpenCode {
				b, err := os.ReadFile(filepath.Join(home, ".local/share/lydiabox/handoff", id+".json"))
				if err != nil || !bytes.Contains(b, []byte("## Original goal")) {
					t.Fatalf("opencode import file: %v", err)
				}
				return
			}
			tr, err := a.ParseTranscript(home, id)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(tr.OriginalGoal, "## Original goal") || tr.WorkspaceDir != "/workspace/proj" {
				t.Fatalf("seed trajectory = %+v", tr)
			}
			ss, err := a.DiscoverSessions(home)
			if err != nil || len(ss) != 1 || ss[0].NativeID != id {
				t.Fatalf("seed not discovered: %+v %v", ss, err)
			}
			if err := a.Purge(home, id); err != nil {
				t.Fatal(err)
			}
			if ss, _ := a.DiscoverSessions(home); len(ss) != 0 {
				t.Fatalf("purge left %+v", ss)
			}
		})
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
		if _, err := a.ParseTranscript(home, ids[a.Name()]); err == nil {
			t.Errorf("%s: purged session still parses", a.Name())
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
		{adapters.Claude{}, harness.LaunchOptions{Model: "kimi/kimi-k3"}, "claude --session-id", true},
		{adapters.Claude{}, harness.LaunchOptions{ResumeNativeID: "abc"}, "claude --resume abc", true},
		{adapters.Codex{}, harness.LaunchOptions{ResumeNativeID: "abc", Model: "m/x"}, "codex resume abc --model m/x", true},
		{adapters.Codex{}, harness.LaunchOptions{}, "codex", false},
		{adapters.DSH{}, harness.LaunchOptions{Model: "deepseek/x"}, "env DSH_MODEL=deepseek/x dsh tui", false},
		{adapters.Antigravity{}, harness.LaunchOptions{ResumeNativeID: "c1"}, "agy --conversation c1", true},
		{adapters.OpenCode{}, harness.LaunchOptions{Model: "kimi/kimi-k3"}, "opencode --model lydiabox/kimi/kimi-k3", false},
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
