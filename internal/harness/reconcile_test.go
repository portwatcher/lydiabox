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

func newManager(t *testing.T, e *env, runner *harnesstest.Runner, w harness.VMWaker) *harness.Manager {
	t.Helper()
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: localFiles(e.home, e.workspace),
		Discovery: localDiscovery(e.home, e.workspace), Store: e.store, Runner: runner, Waker: w, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}

func sessionByID(t *testing.T, mgr *harness.Manager, id string) harness.Session {
	t.Helper()
	list, err := mgr.List(context.Background(), tenant, harness.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("session %s not listed", id)
	return harness.Session{}
}

// After a host restart (a new manager on the same store and guest),
// Reconcile makes a session whose guest terminal still runs live again on
// the same process, marks a session whose terminal ended or is gone
// exited with its end time, and ends a LydiaBox terminal that no session
// refers to.
func TestReconcileAfterAHostRestart(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &harnesstest.Waker{}
	before := newManager(t, e, runner, w)
	alive, err := before.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	ended, err := before.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	ptys := runner.PTYs()
	alivePTY, endedPTY := ptys[0], ptys[1]
	orphan := runner.AddPTY("lbx-orphan")
	foreign := runner.AddPTY("user-tmux")

	// The host process ends: its streams are lost, the guest keeps the
	// processes. One of them ends while the host is down.
	alivePTY.Lose()
	endedPTY.Lose()
	time.Sleep(50 * time.Millisecond)
	endedPTY.Exit(0)
	endAt := time.Now().UTC()

	after := newManager(t, e, runner, w)
	if s := sessionByID(t, after, alive.ID); s.Live {
		t.Fatalf("live before the reconcile: %+v", s)
	}
	if err := after.Reconcile(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if s := sessionByID(t, after, alive.ID); !s.Live || s.Status != harness.StatusRunning {
		t.Fatalf("running terminal after the reconcile: %+v", s)
	}
	room, err := after.Room(ctx, tenant, alive.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, _, _ := room.Subscribe()
	alivePTY.Emit([]byte("SAME-PROCESS"))
	waitData(t, sub, "SAME-PROCESS")
	s := sessionByID(t, after, ended.ID)
	if s.Status != harness.StatusExited || s.UpdatedAt.Sub(endAt).Abs() > time.Second {
		t.Fatalf("ended terminal: status %s, time %s, want %s", s.Status, s.UpdatedAt, endAt)
	}
	// A terminal that ended by its own exit is not one that the VM ended.
	if _, err := after.Room(ctx, tenant, ended.ID); !errors.Is(err, harness.ErrConflict) || !strings.Contains(err.Error(), "has no running terminal") {
		t.Fatalf("room of a terminal that exited: %v", err)
	}
	if !orphan.Exited() || foreign.Exited() {
		t.Fatalf("orphan ended %v, foreign ended %v; want true, false", orphan.Exited(), foreign.Exited())
	}
	if len(runner.PTYs()) != 4 {
		t.Fatalf("%d ptys: the reconcile started a terminal", len(runner.PTYs()))
	}
	// A second reconcile changes nothing.
	if err := after.Reconcile(ctx, tenant); err != nil || !sessionByID(t, after, alive.ID).Live || alivePTY.Exited() {
		t.Fatalf("second reconcile: %v", err)
	}
}

// Deleting a session whose terminal is not attached (after a host
// restart) ends its guest process.
func TestDeleteEndsADetachedGuestTerminal(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &harnesstest.Waker{}
	s, err := newManager(t, e, runner, w).Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	runner.Last().Lose()
	after := newManager(t, e, runner, w)
	if err := after.Delete(ctx, tenant, s.ID, false); err != nil {
		t.Fatal(err)
	}
	if !runner.Last().Exited() {
		t.Fatal("the guest process of the deleted session still runs")
	}
}

// An ended session shows its end time, not its start.
func TestExitedSessionShowsItsEndTime(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	mgr := newManager(t, e, runner, &harnesstest.Waker{})
	s, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	room, err := mgr.Room(ctx, tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	sub, _, _, _ := room.Subscribe()
	time.Sleep(1100 * time.Millisecond)
	runner.Last().Exit(0)
	lastControl(t, sub)
	end := time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := sessionByID(t, mgr, s.ID)
		if got.Status == harness.StatusExited {
			if got.UpdatedAt.Sub(end).Abs() > 500*time.Millisecond {
				t.Fatalf("exited session time %s, want about %s", got.UpdatedAt, end)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session not exited: %+v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// When the VM stops, a session whose terminal was detached (by a pause or
// a host restart) is exited too.
func TestStoppedVMEndsDetachedSessions(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &harnesstest.Waker{}
	s, err := newManager(t, e, runner, w).Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	runner.Last().Lose()
	after := newManager(t, e, runner, w)
	if got := sessionByID(t, after, s.ID); got.Status == harness.StatusExited {
		t.Fatalf("exited before the stop: %+v", got)
	}
	after.Stopped(ctx, tenant)
	if got := sessionByID(t, after, s.ID); got.Status != harness.StatusExited {
		t.Fatalf("after the stop: %+v", got)
	}
	// A client that opens the terminal gets the same answer as before the
	// end was recorded: the VM ended the terminal. No terminal starts.
	if _, err := after.Room(ctx, tenant, s.ID); !errors.Is(err, harness.ErrConflict) || !strings.Contains(err.Error(), "ended while the VM was paused or stopped") {
		t.Fatalf("room of a terminal that the VM ended: %v", err)
	}
	if len(runner.PTYs()) != 1 {
		t.Fatalf("%d ptys: the room started a terminal", len(runner.PTYs()))
	}
}

// markExitedInStore records a session as exited while its process runs, as
// an older host version did at a restart.
func markExitedInStore(t *testing.T, e *env, id string) {
	t.Helper()
	h, native, err := harness.SplitSessionID(id)
	if err != nil {
		t.Fatal(err)
	}
	metas, err := e.store.ListSessionMeta(context.Background(), tenant, h)
	if err != nil {
		t.Fatal(err)
	}
	for _, sm := range metas {
		if sm.SessionID == native {
			sm.Status = harness.StatusExited
			if err := e.store.UpsertSessionMeta(context.Background(), tenant, sm); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no meta for %s", id)
}

// A session recorded as exited whose guest process still runs: Reconcile
// ends the process.
func TestReconcileEndsTheProcessOfAnExitedSession(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &harnesstest.Waker{}
	s, err := newManager(t, e, runner, w).Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	runner.Last().Lose()
	markExitedInStore(t, e, s.ID)
	after := newManager(t, e, runner, w)
	if err := after.Reconcile(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if !runner.Last().Exited() {
		t.Fatal("the process of the exited session still runs")
	}
	if got := sessionByID(t, after, s.ID); got.Status != harness.StatusExited || got.Live {
		t.Fatalf("session after the reconcile: %+v", got)
	}
}

// Deleting a session recorded as exited ends its guest process if it runs.
func TestDeleteEndsTheProcessOfAnExitedSession(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &harnesstest.Waker{}
	s, err := newManager(t, e, runner, w).Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	runner.Last().Lose()
	markExitedInStore(t, e, s.ID)
	if err := newManager(t, e, runner, w).Delete(ctx, tenant, s.ID, false); err != nil {
		t.Fatal(err)
	}
	if !runner.Last().Exited() {
		t.Fatal("the process of the deleted exited session still runs")
	}
}

// An automatic reconnect does not wake a paused VM; an explicit attach
// (opening the terminal) does.
func TestReconnectDoesNotWakeAPausedVM(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newEnv(t, runner)
	ctx := context.Background()
	w := &pauseWaker{Waker: &harnesstest.Waker{}}
	mgr := newManager(t, e, runner, w)
	s, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/home/agent"})
	if err != nil {
		t.Fatal(err)
	}
	w.paused.Store(true)
	mgr.Paused(tenant)
	wakes := w.Wakes
	if _, err := mgr.Reconnect(ctx, tenant, s.ID); !errors.Is(err, harness.ErrVMPaused) {
		t.Fatalf("reconnect of a paused VM: %v", err)
	}
	if w.Wakes != wakes || !w.paused.Load() {
		t.Fatalf("the reconnect woke the VM: wakes %d -> %d", wakes, w.Wakes)
	}
	if _, err := mgr.Room(ctx, tenant, s.ID); err != nil || w.paused.Load() {
		t.Fatalf("explicit attach: %v, paused %v", err, w.paused.Load())
	}
	// A live terminal is returned by a reconnect while the VM runs.
	if _, err := mgr.Reconnect(ctx, tenant, s.ID); err != nil {
		t.Fatal(err)
	}
}
