package harness_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

// pauseWaker is a VM controller that can be paused; a wake resumes it.
type pauseWaker struct {
	*harnesstest.Waker
	paused atomic.Bool
}

func (w *pauseWaker) Paused(string) bool { return w.paused.Load() }

func (w *pauseWaker) EnsureRunning(ctx context.Context, tid string) error {
	w.paused.Store(false)
	return w.Waker.EnsureRunning(ctx, tid)
}

// waitData reads frames until one carries want.
func waitData(t *testing.T, sub *harness.Subscriber, want string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-sub.C:
			if !ok {
				t.Fatalf("the room ended before %q", want)
			}
			if string(f.Data) == want {
				return
			}
		case <-timeout:
			t.Fatalf("no %q", want)
		}
	}
}

// A Hibernate cuts the stream of a live terminal, but the process lives on
// in the paused guest: the room ends with "paused" (not a fake exit code),
// the session is not exited, and the next attach wakes the VM and shows
// the same process.
func TestTerminalCutByAPauseAttachesAgain(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &pauseWaker{Waker: &harnesstest.Waker{}}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace), Store: e.store, Runner: runner, Waker: w, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := mgr.Room(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, _, _ := room.Subscribe()
	pty := runner.Last()
	w.paused.Store(true)
	pty.Lose()
	if got, ok := lastControl(t, sub).(harness.CtlError); !ok || got.Code != harness.CodeVMPaused {
		t.Fatalf("last frame after the pause: %#v", got)
	}
	if n := w.Leases("pty"); n != 0 {
		t.Fatalf("pty leases while paused: %d", n)
	}
	got, err := mgr.Get(ctx, tenant, s.ID)
	if err != nil || got.Status == harness.StatusExited || got.Live {
		t.Fatalf("session while paused: %+v %v", got, err)
	}

	wakes := w.Wakes
	room2, err := mgr.Room(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w.Wakes != wakes+1 || w.paused.Load() || len(runner.PTYs()) != 1 {
		t.Fatalf("attach: wakes %d -> %d, paused %v, ptys %d", wakes, w.Wakes, w.paused.Load(), len(runner.PTYs()))
	}
	sub2, _, _, ok := room2.Subscribe()
	if !ok {
		t.Fatal("subscribe after the wake")
	}
	pty.Emit([]byte("STILL-HERE"))
	waitData(t, sub2, "STILL-HERE")
	if n := w.Leases("pty"); n != 1 {
		t.Fatalf("pty leases after the wake: %d", n)
	}

	// A process that ended during the pause still reports its exit code.
	w.paused.Store(true)
	pty.Exit(3)
	if got, ok := lastControl(t, sub2).(harness.CtlExit); !ok || got.Code != 3 {
		t.Fatalf("exit during a pause: %#v", got)
	}
}

// A stream that ends while the VM runs is an exit, as before.
func TestTerminalLostWhileRunningExits(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &pauseWaker{Waker: &harnesstest.Waker{}}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace), Store: e.store, Runner: runner, Waker: w, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := mgr.Room(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, _, _ := room.Subscribe()
	runner.Last().Lose()
	if got, ok := lastControl(t, sub).(harness.CtlExit); !ok || got.Code != -1 {
		t.Fatalf("last frame: %#v", got)
	}
}
