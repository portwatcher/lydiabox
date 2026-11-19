package harness_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

// memoryWiring also remembers models (as magpie.Bindings does).
type memoryWiring struct {
	fakeWiring
	remembered []string
	fail       bool
}

func (m *memoryWiring) RememberModel(_ context.Context, _, h, model string) error {
	if m.fail {
		return errors.New("not a provider/model ID")
	}
	m.remembered = append(m.remembered, h+"="+model)
	return nil
}

// TestSetModelResumesWithNewModel: a model change ends the harness process
// and resumes the same native session with the new model; the start
// remembers the model of the harness.
func TestSetModelResumesWithNewModel(t *testing.T) {
	e := newEnv(t, &harnesstest.Runner{})
	wiring := &memoryWiring{}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace), Store: e.store, Runner: e.runner, Waker: e.waker, Wiring: wiring, Logger: quiet(),
		KillGrace: 200 * time.Millisecond, TermGrace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.ClaudeCode, WorkspaceDir: "/workspace/lydiabox",
		Model: "claude/claude-sonnet-4-5", Title: "Uploads", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	first := e.runner.Last()
	got, err := mgr.SetModel(ctx, tenant, s.ID, harness.ModelRequest{Model: "claude/claude-opus-4-5"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != s.ID || got.Model != "claude/claude-opus-4-5" || !got.Live || got.Title != "Uploads" {
		t.Fatalf("after the change: %+v", got)
	}
	if sig := first.Signals(); len(sig) == 0 || sig[0] != "SIGINT" {
		t.Fatalf("old process signals = %v", sig)
	}
	second := e.runner.Last()
	want := "claude --dangerously-skip-permissions --resume " + s.NativeID + " --model claude/claude-opus-4-5"
	if second == first || strings.Join(second.Spec.Command, " ") != want || second.Spec.Cols != 100 || second.Spec.Rows != 30 {
		t.Fatalf("new command = %q (%dx%d), want %q", strings.Join(second.Spec.Command, " "), second.Spec.Cols, second.Spec.Rows, want)
	}
	if strings.Join(wiring.remembered, ",") != "claude-code=claude/claude-sonnet-4-5,claude-code=claude/claude-opus-4-5" {
		t.Fatalf("remembered = %v", wiring.remembered)
	}
	first.Exit(0)
	eventually(t, "the old process end", func() bool { return first.Exited() })
	if m, ok := e.store.Get(tenant, harness.ClaudeCode, s.NativeID); !ok || m.Status != harness.StatusRunning || m.Model != "claude/claude-opus-4-5" {
		t.Fatalf("stored = %+v, %v", m, ok)
	}

	// A model that cannot be remembered still starts, with the stored wiring.
	wiring.fail = true
	if _, err := mgr.SetModel(ctx, tenant, s.ID, harness.ModelRequest{Model: "plain-model"}); err != nil || len(wiring.wired) == 0 {
		t.Fatalf("set an unremembered model: %v, wired %v", err, wiring.wired)
	}
	for _, tc := range []struct {
		id, model string
		want      error
	}{
		{s.ID, "", harness.ErrBadRequest},
		{s.ID, "a b", harness.ErrBadRequest},
		{"codex:missing", "x/y", harness.ErrNotFound},
	} {
		if _, err := mgr.SetModel(ctx, tenant, tc.id, harness.ModelRequest{Model: tc.model}); !errors.Is(err, tc.want) {
			t.Errorf("SetModel(%s, %q) = %v, want %v", tc.id, tc.model, err, tc.want)
		}
	}
	// A provisional session (Codex before its first message) cannot resume yet.
	c, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Codex, WorkspaceDir: "/workspace/lydiabox"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.SetModel(ctx, tenant, c.ID, harness.ModelRequest{Model: "x/y"}); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("provisional = %v", err)
	}
}
