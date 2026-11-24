package harness_test

import (
	"context"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/harnesstest"
)

// TestProvisionalHubDoesNotWakeAfterAPause watches a live provisional
// session, then pauses the machine (the terminal detaches). The hub must
// not ask for a wake: only the open of a session wakes the machine, and a
// wake after the pause would undo it.
func TestProvisionalHubDoesNotWakeAfterAPause(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newCacheEnvWith(t, true, func(o *harness.ManagerOptions) { o.Runner = runner })
	id := "antigravity:new-1"
	storeProvisional(t, e.store, harness.Antigravity, "new-1", "lbx-agy")
	pty := runner.AddPTY("lbx-agy")
	if err := e.mgr.Reconcile(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	c.follow(t, sub, "the live terminal", func() bool { return c.status.Live })

	e.disc.Pause()
	pty.Lose()
	c.follow(t, sub, "the detached terminal", func() bool { return !c.status.Live })
	// Many polls of the session that is not live.
	c.drain(sub, 300*time.Millisecond)
	if n := e.disc.Wakes(); n != 0 {
		t.Fatalf("the hub asked for %d wakes after the pause, want none", n)
	}
	if !c.status.Synced || c.status.Error != "" {
		t.Fatalf("status after the pause = %+v", c.status)
	}
}

// TestACPHubDoesNotWakeAfterAPause watches a session that an ACP bridge
// runs, then pauses the machine: the stream of the bridge ends, and the
// read of the transcript that follows must not wake the machine.
func TestACPHubDoesNotWakeAfterAPause(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newCacheEnvWith(t, true, func(o *harness.ManagerOptions) { o.Runner = runner })
	ctx := context.Background()
	s, err := e.mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Codex, WorkspaceDir: "/workspace",
		Engine: harness.EngineACP, ResumeNativeID: "native-1"})
	if err != nil {
		t.Fatal(err)
	}
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	c.follow(t, sub, "the bridge", func() bool { return c.status.Live })

	e.disc.Pause()
	runner.Last().Lose()
	c.follow(t, sub, "the end of the bridge", func() bool { return !c.status.Live })
	c.drain(sub, 300*time.Millisecond)
	if n := e.disc.Wakes(); n != 0 {
		t.Fatalf("the hub asked for %d wakes after the pause, want none", n)
	}
}

// TestHubWakesAtTheOpen: the first poll of a hub asks for the wake (the
// user opened the session), and only that poll.
func TestHubWakesAtTheOpen(t *testing.T) {
	e := newCacheEnvWith(t, false, func(*harness.ManagerOptions) {})
	e.disc.SetSession(harness.Codex, "native-2", time.Now(), ev("a", "one"))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:native-2")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	eventually(t, "the wake of the open", func() bool { return e.disc.Wakes() == 1 })
	e.disc.Resume()
	c.follow(t, sub, "the events", func() bool { return c.status.Synced && len(c.order) == 1 })
	e.disc.Pause()
	c.drain(sub, 300*time.Millisecond)
	if n := e.disc.Wakes(); n != 1 {
		t.Fatalf("wakes = %d, want 1", n)
	}
}
