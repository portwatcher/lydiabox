package harness_test

import (
	"context"
	"errors"
	"github.com/lydiabox/lydiabox/internal/agentfs"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

const tenant = "default"

type env struct {
	home, workspace string
	runner          *harnesstest.Runner
	waker           *harnesstest.Waker
	store           *harnesstest.Store
	wiring          *fakeWiring
	mgr             *harness.Manager
}

type fakeWiring struct {
	wired []string
}

func (f *fakeWiring) EnsureWired(_ context.Context, _, h string) error {
	f.wired = append(f.wired, h)
	return nil
}
func (f *fakeWiring) Env(h string) map[string]string {
	if h == harness.Codex {
		return map[string]string{"LYDIABOX_GATEWAY_TOKEN": "lydiabox-local"}
	}
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newEnv(t *testing.T, runner harness.PTYRunner) *env {
	t.Helper()
	home := adapterstest.Home(t)
	ws := filepath.Join(filepath.Dir(filepath.Dir(home)), "workspace")
	if err := os.MkdirAll(filepath.Join(ws, "lydiabox"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &env{home: home, workspace: ws, waker: &harnesstest.Waker{}, store: &harnesstest.Store{}, wiring: &fakeWiring{}}
	if r, ok := runner.(*harnesstest.Runner); ok {
		e.runner = r
	}
	mgr, err := harness.NewManager(harness.ManagerOptions{
		Adapters: adapters.All(), Files: localFiles(home, ws), Discovery: localDiscovery(home, ws), Store: e.store,
		Runner: runner, Waker: e.waker, Wiring: e.wiring, Logger: quiet(),
		KillGrace: 200 * time.Millisecond, TermGrace: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mgr = mgr
	return e
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeDSHSession writes a plain-text dsh session file with one prompt.
func writeDSHSession(t *testing.T, home, workspace, id string) {
	t.Helper()
	dir := filepath.Join(home, ".dsh/sessions", "--"+strings.ReplaceAll(workspace, "/", "-")+"--", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	data := `{"type":"session","id":"` + id + `","createdAt":` + now + `,"cwd":"` + workspace + `"}` + "\n" +
		`{"type":"user/message","seq":1,"time":` + now + `,"data":{"role":"user","content":[{"type":"text","text":"x"}],"source":{"kind":"user"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestListWhileVMStopped proves discovery reads the host home directory
// with no VM: the runner is unavailable.
func TestListWhileVMStopped(t *testing.T) {
	e := newEnv(t, harness.UnavailableRunner{})
	ctx := context.Background()
	all, err := e.mgr.List(ctx, tenant, harness.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"claude-code:" + adapterstest.ClaudeID: true, "codex:" + adapterstest.CodexID: true, "codex:" + adapterstest.CodexLegacyID: true,
		"dsh:" + adapterstest.DSHID: true, "antigravity:" + adapterstest.AntigravityID: true,
		"opencode:" + adapterstest.OpenCodeID: true, "opencode:" + adapterstest.OpenCodeLegacy: true,
	}
	if len(all) != len(want) {
		t.Fatalf("listed %d sessions, want %d: %+v", len(all), len(want), all)
	}
	for i, s := range all {
		if !want[s.ID] || s.Status != harness.StatusIdle || s.Live {
			t.Errorf("unexpected session %+v", s)
		}
		if i > 0 && s.UpdatedAt.After(all[i-1].UpdatedAt) {
			t.Error("sessions are not newest first")
		}
	}
	codex, err := e.mgr.List(ctx, tenant, harness.Filter{Harness: harness.Codex, Workspace: adapterstest.Workspace})
	if err != nil || len(codex) != 1 || codex[0].Usage == nil || codex[0].Usage.InputTokens != 182344 {
		t.Fatalf("filtered list = %+v, %v", codex, err)
	}
	if _, err := e.mgr.List(ctx, tenant, harness.Filter{Harness: "nope"}); !errors.Is(err, harness.ErrBadRequest) {
		t.Fatalf("unknown harness filter: %v", err)
	}
	_, err = e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.ClaudeCode, WorkspaceDir: adapterstest.Workspace})
	if !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("create without a runner = %v, want ErrUnavailable", err)
	}
}

func TestCreateDeleteAndLeases(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	ctx := context.Background()
	s, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Codex, WorkspaceDir: "/workspace/lydiabox/sub",
		Model: "deepseek/deepseek-v4-pro", Title: "Refactor", Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Live || s.Status != harness.StatusRunning || !strings.HasPrefix(s.NativeID, "new-") || s.Title != "Refactor" {
		t.Fatalf("created %+v", s)
	}
	p := e.runner.Last()
	if got := strings.Join(p.Spec.Command, " "); got != "codex --model deepseek/deepseek-v4-pro" {
		t.Fatalf("command = %q", got)
	}
	if p.Spec.Dir != "/workspace/lydiabox/sub" || p.Spec.Cols != 120 || p.Spec.Env["LYDIABOX_GATEWAY_TOKEN"] != "lydiabox-local" ||
		p.Spec.Env["LYDIABOX_SESSION_ID"] != s.ID {
		t.Fatalf("spec = %+v", p.Spec)
	}
	if _, err := os.Stat(filepath.Join(e.workspace, "lydiabox/sub")); err != nil {
		t.Fatal("workspace directory not created on the host")
	}
	if e.waker.Wakes != 1 || e.waker.Leases("pty") != 1 || len(e.wiring.wired) != 1 {
		t.Fatalf("wakes=%d leases=%d wired=%v", e.waker.Wakes, e.waker.Leases("pty"), e.wiring.wired)
	}
	if got, err := e.mgr.Get(ctx, tenant, s.ID); err != nil || !got.Live {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if err := e.mgr.Delete(ctx, tenant, s.ID, false); err != nil {
		t.Fatal(err)
	}
	if sig := p.Signals(); len(sig) != 1 || sig[0] != "SIGINT" {
		t.Fatalf("signals = %v", sig)
	}
	eventually(t, "lease release", func() bool { return e.waker.Leases("pty") == 0 })
	if _, err := e.mgr.Get(ctx, tenant, s.ID); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("deleted session still listed: %v", err)
	}

	// A harness that ignores SIGINT gets SIGTERM after the grace period.
	e.runner.IgnoreSIGINT = true
	s2, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.ClaudeCode, WorkspaceDir: adapterstest.Workspace})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(s2.NativeID, "new-") {
		t.Fatal("claude sessions get their ID up front")
	}
	p2 := e.runner.Last()
	if err := e.mgr.Delete(ctx, tenant, s2.ID, false); err != nil {
		t.Fatal(err)
	}
	if sig := p2.Signals(); strings.Join(sig, ",") != "SIGINT,SIGTERM" {
		t.Fatalf("signals = %v", sig)
	}

	// An interactive shell ignores SIGINT and SIGTERM; SIGHUP ends it and
	// releases its lease.
	e.runner.IgnoreSIGTERM = true
	s3, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell})
	if err != nil {
		t.Fatal(err)
	}
	p3 := e.runner.Last()
	if err := e.mgr.Delete(ctx, tenant, s3.ID, false); err != nil {
		t.Fatal(err)
	}
	if sig := p3.Signals(); strings.Join(sig, ",") != "SIGINT,SIGTERM,SIGHUP" {
		t.Fatalf("shell signals = %v", sig)
	}
	eventually(t, "shell lease release", func() bool { return e.waker.Leases("pty") == 0 })
	e.runner.IgnoreSIGINT, e.runner.IgnoreSIGTERM = false, false

	for _, bad := range []harness.CreateRequest{{Harness: "vim"}, {Harness: harness.Codex, WorkspaceDir: "/etc"},
		{Harness: harness.Codex, WorkspaceDir: "/workspace/../etc"}, {Harness: harness.Shell, ResumeNativeID: "x"}} {
		if _, err := e.mgr.Create(ctx, tenant, bad); !errors.Is(err, harness.ErrBadRequest) {
			t.Errorf("Create(%+v) = %v, want ErrBadRequest", bad, err)
		}
	}
}

func TestPurgeDeletesTranscript(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	ctx := context.Background()
	id := "dsh:" + adapterstest.DSHID
	if err := e.mgr.Delete(ctx, tenant, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".dsh/sessions/---workspace-lydiabox--", adapterstest.DSHID)); !os.IsNotExist(err) {
		t.Fatalf("transcript not purged: %v", err)
	}
	if err := e.mgr.Delete(ctx, tenant, id, true); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestProvisionalSessionAdoptsNativeID(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	ctx := context.Background()
	s, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.DSH, WorkspaceDir: "/workspace/fresh"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := e.mgr.Room(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// dsh writes its session file after it starts.
	nativeID := "session-0f3c9a2e-5d6b-4e7f-8a9b-0c1d2e3f4a5b"
	writeDSHSession(t, e.home, "/workspace/fresh", nativeID)
	all, err := e.mgr.List(ctx, tenant, harness.Filter{Workspace: "/workspace/fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].NativeID != nativeID || !all[0].Live {
		t.Fatalf("after adoption: %+v", all)
	}
	if room.SessionID() != "dsh:"+nativeID {
		t.Fatalf("room session = %s", room.SessionID())
	}
	if _, err := e.mgr.Room(ctx, tenant, s.ID); err != nil {
		t.Fatalf("old provisional ID no longer resolves: %v", err)
	}
	if _, ok := e.store.Get(tenant, harness.DSH, s.NativeID); ok {
		t.Fatal("provisional metadata kept")
	}
}

func TestRoomReattachAfterRestart(t *testing.T) {
	runner := &harnesstest.Runner{Replay: []byte("previous output")}
	e := newEnv(t, runner)
	ctx := context.Background()
	s, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	// A new manager (host restart) with the same store and guest.
	mgr2, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace),
		Store:     e.store, Runner: runner, Waker: e.waker, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	room, err := mgr2.Room(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, hello, replay, ok := room.Subscribe()
	if !ok || string(replay) != "previous output" || hello.SessionID != s.ID {
		t.Fatalf("re-attached room: %+v %q", hello, replay)
	}
	room.Unsubscribe(sub)
	runner.Last().Exit(0)
	eventually(t, "exit recorded", func() bool {
		m, _ := e.store.Get(tenant, harness.Shell, s.NativeID)
		return m.Status == harness.StatusExited
	})
	// Both managers watch the same fake PTY, so the store can show the exit
	// (written by the first manager) before mgr2 has dropped its live room.
	eventually(t, "exited session room gives ErrConflict", func() bool {
		_, err := mgr2.Room(ctx, tenant, s.ID)
		return errors.Is(err, harness.ErrConflict)
	})
}

func localFiles(home, ws string) agentfs.FS {
	return agentfs.NewLocal(harnesstest.Dirs{Home: home, Workspace: ws}, agentfs.LocalOptions{})
}

func localDiscovery(home, ws string) harness.Discovery {
	return harness.LocalDiscovery{Dirs: harnesstest.Dirs{Home: home, Workspace: ws}, Adapters: adapters.All()}
}
