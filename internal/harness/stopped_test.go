package harness_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
	"github.com/portwatcher/lydiabox/internal/harness/harnesstest"
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

func (w *holdWaker) Held(string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held
}

func (w *holdWaker) setHeld(v bool) {
	w.mu.Lock()
	w.held = v
	w.mu.Unlock()
}

// lastControl reads the frames of a room until it ends and returns the
// last control frame.
func lastControl(t *testing.T, sub *harness.Subscriber) any {
	t.Helper()
	var last any
	timeout := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-sub.C:
			if !ok {
				return last
			}
			if f.Control != nil {
				last = f.Control
			}
		case <-timeout:
			t.Fatal("the room did not end")
		}
	}
}

// A live terminal that ends because the user stopped the VM ends with the
// "stopped" error, so the clients show the stopped state; an ordinary end
// still sends the exit frame.
func TestTerminalEndedByAUserStopSaysStopped(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &holdWaker{Waker: &harnesstest.Waker{}}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace), Store: e.store, Runner: runner, Waker: w, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	for _, held := range []bool{true, false} {
		w.setHeld(false)
		s, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
		if err != nil {
			t.Fatal(err)
		}
		room, err := mgr.Room(ctx, tenant, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		sub, _, _, ok := room.Subscribe()
		if !ok {
			t.Fatal("subscribe")
		}
		w.setHeld(held)
		runner.Last().Exit(-1)
		got := lastControl(t, sub)
		if held {
			if e, ok := got.(harness.CtlError); !ok || e.Code != harness.CodeVMStopped || harness.CodeVMStopped != api.CodeVMStopped {
				t.Fatalf("held: last frame %#v", got)
			}
		} else if _, ok := got.(harness.CtlExit); !ok {
			t.Fatalf("not held: last frame %#v", got)
		}
	}
}
