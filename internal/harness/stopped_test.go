package harness_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

// holdWaker is a VM controller with the user-stop hold: EnsureRunning
// refuses a held VM, Start lifts the hold.
type holdWaker struct {
	*harnesstest.Waker
	mu     sync.Mutex
	held   bool
	starts int
}

func (w *holdWaker) EnsureRunning(ctx context.Context, tid string) error {
	w.mu.Lock()
	held := w.held
	w.mu.Unlock()
	if held {
		return api.ErrVMStopped
	}
	return w.Waker.EnsureRunning(ctx, tid)
}

func (w *holdWaker) Start(ctx context.Context, tid string) error {
	w.mu.Lock()
	w.held = false
	w.starts++
	w.mu.Unlock()
	return w.Waker.EnsureRunning(ctx, tid)
}

// A terminal socket that reconnects to a session after the user stopped
// the VM does not start the VM; a new session (an explicit request) does.
func TestStoppedVMIsStartedOnlyByANewSession(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	s, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	w := &holdWaker{Waker: &harnesstest.Waker{}, held: true}
	mgr2, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace), Store: e.store, Runner: runner, Waker: w, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mgr2.Room(ctx, tenant, s.ID)
	if !errors.Is(err, api.ErrVMStopped) || !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("room of a held VM: %v", err)
	}
	if w.Wakes != 0 || w.starts != 0 {
		t.Fatalf("wakes %d starts %d", w.Wakes, w.starts)
	}

	s2, err := mgr2.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	if w.starts != 1 || w.held {
		t.Fatalf("create: starts %d held %v", w.starts, w.held)
	}
	if _, err := mgr2.Room(ctx, tenant, s2.ID); err != nil {
		t.Fatal(err)
	}
	runner.Last().Exit(0)
}
