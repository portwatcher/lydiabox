package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

type fakeGit struct{ status, diff string }

func (g fakeGit) Inspect(context.Context, string) (string, string, error) {
	return g.status, g.diff, nil
}

const userAgents = "# Project rules\n\nRun `make lint` before every commit.\n"

func newBridge(e *env) *harness.Bridge {
	return &harness.Bridge{Manager: e.mgr, Binder: e.wiring, Logger: quiet(),
		Git: fakeGit{status: " M internal/storage/files/http.go\n?? internal/storage/files/resume.go",
			diff: " internal/storage/files/http.go | 15 ++++++++++++---\n 1 file changed, 12 insertions(+), 3 deletions(-)"},
		Now: func() time.Time { return time.Date(2026, 11, 10, 6, 30, 0, 0, time.UTC) }}
}

// resumeSource starts a live terminal for a discovered fixture session.
func resumeSource(t *testing.T, e *env, h, nativeID string) (harness.Session, *harnesstest.PTY) {
	t.Helper()
	s, err := e.mgr.Create(context.Background(), tenant, harness.CreateRequest{Harness: h, WorkspaceDir: adapterstest.Workspace,
		ResumeNativeID: nativeID})
	if err != nil {
		t.Fatal(err)
	}
	if want := h + ":" + nativeID; s.ID != want && !strings.HasPrefix(s.NativeID, "new-") {
		t.Fatalf("resumed %s, want %s", s.ID, want)
	}
	return s, e.runner.Last()
}

func TestHotSwitchPairs(t *testing.T) {
	cases := []struct {
		from, fromID, to, model string
		goal, issue, modified   string
		cmd                     string
	}{
		{harness.ClaudeCode, adapterstest.ClaudeID, harness.DSH, "deepseek/deepseek-v4-pro",
			"Make uploads in the File Manager resumable.", "run go test ./internal/storage/... failed", "internal/storage/files/http.go", "dsh tui --resume session-"},
		{harness.Codex, adapterstest.CodexID, harness.OpenCode, "kimi/kimi-k3",
			"Fix the flaky upload test in internal/storage/files.", "", "internal/storage/files/service_test.go", "opencode import"},
		{harness.Antigravity, adapterstest.AntigravityID, harness.ClaudeCode, "",
			"Add a dark mode toggle to the web UI header.", "run npm run build failed", "web/src/components/ThemeToggle.tsx", "claude --resume "},
		{harness.DSH, adapterstest.DSHID, harness.Codex, "openai/gpt-5.2",
			"Add retry with exponential backoff", "run go vet ./internal/storage/ failed", "internal/storage/metasync.go", "codex resume "},
		{harness.OpenCode, adapterstest.OpenCodeID, harness.Antigravity, "",
			"Write docs/pty.md", "run markdownlint docs/pty.md failed", "docs/pty.md", "agy --prompt-interactive"},
	}
	for _, tc := range cases {
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			e := newEnv(t, &harnesstest.Runner{})
			agents := filepath.Join(e.workspace, "lydiabox", "AGENTS.md")
			if err := os.WriteFile(agents, []byte(userAgents), 0o644); err != nil {
				t.Fatal(err)
			}
			b := newBridge(e)
			ctx := context.Background()
			src, srcPTY := resumeSource(t, e, tc.from, tc.fromID)
			room, err := e.mgr.Room(ctx, tenant, src.ID)
			if err != nil {
				t.Fatal(err)
			}
			sub, _, _, _ := room.Subscribe()
			srcPTY.Emit([]byte("working...\r\n"))

			// Dry run: preview only.
			prev, err := b.SwitchHarness(ctx, tenant, harness.SwitchRequest{SourceSessionID: src.ID, TargetHarness: tc.to, TargetModel: tc.model, DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if !prev.DryRun || !strings.HasPrefix(prev.HandoffMarkdown, "# Handoff\n\n## Original goal\n") ||
				!strings.Contains(prev.State.OriginalGoal, tc.goal) || prev.State.GitStatus == "" {
				t.Fatalf("preview = %+v", prev)
			}
			if len(srcPTY.Signals()) != 0 {
				t.Fatal("dry run interrupted the source")
			}
			if _, err := os.Stat(filepath.Join(e.workspace, "lydiabox/.agent/HANDOFF.md")); !os.IsNotExist(err) {
				t.Fatal("dry run wrote HANDOFF.md")
			}

			res, err := b.SwitchHarness(ctx, tenant, harness.SwitchRequest{SourceSessionID: src.ID, TargetHarness: tc.to, TargetModel: tc.model})
			if err != nil {
				t.Fatal(err)
			}
			if res.PreviousSessionID != src.ID || res.Session == nil || res.Session.Harness != tc.to || !res.Session.Live ||
				res.HandoffPath != "/workspace/lydiabox/.agent/HANDOFF.md" {
				t.Fatalf("result = %+v", res)
			}
			if sig := srcPTY.Signals(); len(sig) == 0 || sig[0] != "SIGINT" {
				t.Fatalf("source signals = %v", sig)
			}
			tgt := e.runner.Last()
			if got := strings.Join(tgt.Spec.Command, " "); !strings.Contains(got, tc.cmd) || tgt.Spec.Dir != adapterstest.Workspace {
				t.Fatalf("target command %q in %s", got, tgt.Spec.Dir)
			}
			if tc.model != "" && (len(e.wiring.bound) != 1 || e.wiring.bound[0] != tc.to+"="+tc.model) {
				t.Fatalf("model binding = %v", e.wiring.bound)
			}

			// The room announces the switch and streams the new terminal.
			tgt.Emit([]byte("target ready"))
			var sawSwitch, sawOutput bool
			timeout := time.After(5 * time.Second)
			for !sawSwitch || !sawOutput {
				select {
				case f, ok := <-sub.C:
					if !ok {
						t.Fatal("room closed during the switch")
					}
					if c, ok := f.Control.(harness.CtlSwitched); ok {
						sawSwitch = c.FromSessionID == src.ID && c.ToSessionID == res.Session.ID && c.HandoffPath == res.HandoffPath
					}
					if _, ok := f.Control.(harness.CtlExit); ok {
						t.Fatal("clients got exit during a switch")
					}
					if string(f.Data) == "target ready" {
						sawOutput = true
					}
				case <-timeout:
					t.Fatalf("switched=%v output=%v", sawSwitch, sawOutput)
				}
			}
			if room.SessionID() != res.Session.ID {
				t.Fatalf("room shows %s", room.SessionID())
			}

			md := readFile(t, filepath.Join(e.workspace, "lydiabox/.agent/HANDOFF.md"))
			for _, want := range []string{"## Original goal", tc.goal, "## Modified files", "`" + tc.modified + "`",
				"`internal/storage/files/resume.go`", "## Recent tool actions", "## Unresolved issues", tc.issue,
				"git status --short", "1 file changed", "Switched from `" + src.ID + "` to `" + tc.to + "`"} {
				if !strings.Contains(md, want) {
					t.Errorf("HANDOFF.md lacks %q:\n%s", want, md)
				}
			}
			var st map[string]any
			if err := json.Unmarshal([]byte(readFile(t, filepath.Join(e.workspace, "lydiabox/.agent/state.json"))), &st); err != nil {
				t.Fatal(err)
			}
			if st["sourceSessionId"] != src.ID || st["targetHarness"] != tc.to || st["gitDiffStat"] == "" {
				t.Fatalf("state.json = %v", st)
			}
			checkManagedBlocks(t, e)
			if got := readFile(t, agents); !strings.HasPrefix(got, userAgents) {
				t.Fatalf("user AGENTS.md content lost:\n%s", got)
			}
			// The native seed (when the harness has one) is discoverable.
			if a, _ := e.mgr.Adapter(tc.to); tc.to != harness.Antigravity && tc.to != harness.OpenCode {
				if _, err := a.ParseTranscript(e.home, res.Session.NativeID); err != nil {
					t.Fatalf("target seed: %v", err)
				}
			}
			// The source is no longer live; the target is.
			all, _ := e.mgr.List(ctx, tenant, harness.Filter{})
			for _, s := range all {
				if s.ID == src.ID && s.Live {
					t.Error("source still live")
				}
			}
			if e.waker.Leases("pty") != 1 {
				t.Errorf("pty leases = %d, want 1", e.waker.Leases("pty"))
			}
		})
	}
}

func checkManagedBlocks(t *testing.T, e *env) {
	t.Helper()
	for _, name := range harness.InstructionFiles {
		got := readFile(t, filepath.Join(e.workspace, "lydiabox", name))
		if strings.Count(got, harness.BlockStart) != 1 || strings.Count(got, harness.BlockEnd) != 1 ||
			!strings.Contains(got, ".agent/HANDOFF.md") {
			t.Errorf("%s managed block:\n%s", name, got)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSwitchTwiceIsIdempotent switches A -> B -> C on an idle session and
// checks the managed blocks stay single.
func TestSwitchTwiceIsIdempotent(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	b := newBridge(e)
	ctx := context.Background()
	res, err := b.SwitchHarness(ctx, tenant, harness.SwitchRequest{SourceSessionID: "claude-code:" + adapterstest.ClaudeID, TargetHarness: harness.Codex})
	if err != nil {
		t.Fatal(err)
	}
	first := map[string]string{}
	for _, n := range harness.InstructionFiles {
		first[n] = readFile(t, filepath.Join(e.workspace, "lydiabox", n))
	}
	if _, err := b.SwitchHarness(ctx, tenant, harness.SwitchRequest{SourceSessionID: res.Session.ID, TargetHarness: harness.DSH}); err != nil {
		t.Fatal(err)
	}
	for _, n := range harness.InstructionFiles {
		if got := readFile(t, filepath.Join(e.workspace, "lydiabox", n)); got != first[n] {
			t.Errorf("%s changed on the second switch:\n%s", n, got)
		}
	}
	checkManagedBlocks(t, e)
}

func TestSwitchErrors(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	b := newBridge(e)
	ctx := context.Background()
	src := "codex:" + adapterstest.CodexID
	for _, tc := range []struct {
		req  harness.SwitchRequest
		want error
	}{
		{harness.SwitchRequest{SourceSessionID: "codex:missing", TargetHarness: harness.DSH}, harness.ErrNotFound},
		{harness.SwitchRequest{SourceSessionID: src, TargetHarness: harness.Codex}, harness.ErrBadRequest},
		{harness.SwitchRequest{SourceSessionID: src, TargetHarness: "emacs"}, harness.ErrBadRequest},
		{harness.SwitchRequest{SourceSessionID: src, TargetHarness: harness.DSH, TargetModel: "no-slash"}, harness.ErrBadRequest},
	} {
		if _, err := b.SwitchHarness(ctx, tenant, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", tc.req, err, tc.want)
		}
	}
	// Target start failure: the room gets an error and the call is 503.
	src2, _ := resumeSource(t, e, harness.Codex, adapterstest.CodexID)
	room, _ := e.mgr.Room(ctx, tenant, src2.ID)
	sub, _, _, _ := room.Subscribe()
	e.runner.Err = errors.New("vsock: connection reset")
	if _, err := b.SwitchHarness(ctx, tenant, harness.SwitchRequest{SourceSessionID: src2.ID, TargetHarness: harness.DSH}); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("start failure = %v", err)
	}
	var gotErr bool
	for f := range sub.C {
		if c, ok := f.Control.(harness.CtlError); ok && c.Code == "unavailable" {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("room clients did not get the error")
	}
}

func TestUpsertManagedBlock(t *testing.T) {
	once := harness.UpsertManagedBlock("")
	if harness.UpsertManagedBlock(once) != once {
		t.Fatal("not idempotent on a fresh file")
	}
	user := "# Rules\n\nBe careful.\n"
	got := harness.UpsertManagedBlock(user)
	if !strings.HasPrefix(got, user+"\n"+harness.BlockStart) || harness.UpsertManagedBlock(got) != got {
		t.Fatalf("append:\n%s", got)
	}
	stale := "a\n" + harness.BlockStart + "\nold text\n" + harness.BlockEnd + "\nb\n" + harness.BlockStart + "\ndup\n" + harness.BlockEnd + "\n"
	fixed := harness.UpsertManagedBlock(stale)
	if strings.Count(fixed, harness.BlockStart) != 1 || strings.Contains(fixed, "old text") || !strings.HasPrefix(fixed, "a\n") ||
		!strings.Contains(fixed, "\nb") {
		t.Fatalf("replace:\n%s", fixed)
	}
}

// TestExecGitSandbox runs the real git binary against a repository whose
// config tries to run commands (fsmonitor, a clean filter).
func TestExecGitSandbox(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "pwned")
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-m", "a")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.txt filter=evil diff=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, ".git/config")
	f, _ := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("[core]\n\tfsmonitor = touch " + marker + "\n[filter \"evil\"]\n\tclean = touch " + marker + "\n[diff \"evil\"]\n\ttextconv = touch " + marker + "\n")
	f.Close()
	status, diff, err := harness.ExecGit{}.Inspect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "M a.txt") || !strings.Contains(diff, "a.txt") {
		t.Fatalf("status=%q diff=%q", status, diff)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository config ran a command on the host")
	}
	if s, d, err := (harness.ExecGit{}).Inspect(context.Background(), t.TempDir()); err != nil || s != "" || d != "" {
		t.Fatalf("non-repository: %q %q %v", s, d, err)
	}
	inc := t.TempDir()
	run2 := exec.Command("git", "init", "-q", inc)
	if err := run2.Run(); err != nil {
		t.Fatal(err)
	}
	f, _ = os.OpenFile(filepath.Join(inc, ".git/config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("[include]\n\tpath = /etc/gitconfig\n")
	f.Close()
	if _, _, err := (harness.ExecGit{}).Inspect(context.Background(), inc); err == nil {
		t.Fatal("repository with includes was inspected")
	}
}
