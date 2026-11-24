package harness_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/harnesstest"
)

// reattachEnv is a cache env with a runner and a counting waker.
func reattachEnv(t *testing.T, running bool) (*cacheEnv, *harnesstest.Runner, *harnesstest.Waker) {
	t.Helper()
	runner, w := &harnesstest.Runner{}, &harnesstest.Waker{}
	e := newCacheEnvWith(t, running, func(o *harness.ManagerOptions) { o.Runner, o.Waker = runner, w })
	return e, runner, w
}

func watch(t *testing.T, e *cacheEnv, id string) (*harness.EventSub, *client) {
	t.Helper()
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(done)
	c := &client{}
	c.apply(first)
	return sub, c
}

// TestWatchedTerminalAttachedAfterLinkLoss attaches again a terminal that a
// guest link loss detached while the machine kept running, when a client
// watches the session: no wake and no new process.
func TestWatchedTerminalAttachedAfterLinkLoss(t *testing.T) {
	e, runner, w := reattachEnv(t, true)
	storeRunning(t, e.store, harness.Codex, "s1", "lbx-s1")
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"))
	p := runner.AddPTY("lbx-s1")
	if err := e.mgr.Reconcile(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	p.Lose()
	eventually(t, "the detach", func() bool { return !sessionByID(t, e.mgr, "codex:s1").Live })
	wakes, ptys := w.Wakes, len(runner.PTYs())

	sub, c := watch(t, e, "codex:s1")
	c.follow(t, sub, "the live terminal", func() bool { return c.status.Live })
	if s := sessionByID(t, e.mgr, "codex:s1"); !s.Live || s.Status != harness.StatusRunning {
		t.Fatalf("session = %+v", s)
	}
	if w.Wakes != wakes || len(runner.PTYs()) != ptys {
		t.Fatalf("wakes %d -> %d, PTYs %d -> %d", wakes, w.Wakes, ptys, len(runner.PTYs()))
	}
}

// TestWatchedTerminalGone marks a watched session exited when the guest no
// longer has its terminal.
func TestWatchedTerminalGone(t *testing.T) {
	e, _, _ := reattachEnv(t, true)
	storeRunning(t, e.store, harness.Codex, "s2", "lbx-gone")
	e.disc.SetSession(harness.Codex, "s2", time.Now(), ev("u1", "hi"))
	sub, c := watch(t, e, "codex:s2")
	c.follow(t, sub, "the events", func() bool { return c.status.Synced })
	eventually(t, "the exited record", func() bool {
		sm, ok := e.store.Get(tenant, harness.Codex, "s2")
		return ok && sm.Status == harness.StatusExited
	})
	if c.status.Live {
		t.Fatalf("status = %+v", c.status)
	}
}

// TestWatchedTerminalNotAttachedWhileNotServing attaches nothing while the
// machine does not serve (paused, or a pause or stop in progress).
func TestWatchedTerminalNotAttachedWhileNotServing(t *testing.T) {
	e, runner, w := reattachEnv(t, false)
	e.disc.WakeErr = errors.New("the VM was stopped")
	storeRunning(t, e.store, harness.Codex, "s3", "lbx-s3")
	runner.AddPTY("lbx-s3")
	sub, c := watch(t, e, "codex:s3")
	c.drain(sub, 200*time.Millisecond)
	if c.status.Live {
		t.Fatalf("attached while the machine did not serve: %+v", c.status)
	}
	if sm, ok := e.store.Get(tenant, harness.Codex, "s3"); !ok || sm.Status != harness.StatusRunning {
		t.Fatalf("record = %+v", sm)
	}
	if w.Wakes != 0 {
		t.Fatalf("wakes = %d", w.Wakes)
	}
}
