package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/harnesstest"
	"github.com/portwatcher/lydiabox/internal/store"
)

// TestPauseHookMakesNoGuestCall writes the changed copy of a watched session
// from memory before a pause, without a guest call.
func TestPauseHookMakesNoGuestCall(t *testing.T) {
	e := newCacheEnvWith(t, true, func(o *harness.ManagerOptions) {
		o.EventPoll, o.EventCopyDelay = time.Hour, time.Hour
	})
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"), ev("a1", "reply"))
	e.disc.SetSession(harness.Codex, "s2", time.Now(), ev("u1", "not watched"))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	c.follow(t, sub, "the events", func() bool { return c.status.Synced })
	if e.cache.Puts() != 0 {
		t.Fatal("the copy was written before the pause")
	}
	calls := e.disc.GuestCalls()
	e.mgr.SaveEventCopiesBeforePause(context.Background(), tenant)
	if keys, _ := e.cache.Keys(tenant, harness.Codex, "s1"); !slices.Equal(keys, []string{"u1", "a1"}) {
		t.Fatalf("copy = %v", keys)
	}
	if _, ok := e.cache.Keys(tenant, harness.Codex, "s2"); ok {
		t.Fatal("the pause hook read a session that nobody watches")
	}
	if n := e.disc.GuestCalls(); n != calls {
		t.Fatalf("the pause hook made %d guest calls", n-calls)
	}
}

// storeRunning stores a session whose terminal ptyID runs in the guest.
func storeRunning(t *testing.T, st *harnesstest.Store, h, nativeID, ptyID string) {
	t.Helper()
	x, _ := json.Marshal(map[string]any{"ptyId": ptyID})
	err := st.UpsertSessionMeta(context.Background(), tenant, store.SessionMeta{Harness: h, SessionID: nativeID,
		WorkspaceDir: "/workspace", Status: harness.StatusRunning, Extra: x})
	if err != nil {
		t.Fatal(err)
	}
}

// fillEnv has two live codex sessions s1 and s2 with stale copies.
func fillEnv(t *testing.T) (*cacheEnv, *harnesstest.PTY, *harnesstest.PTY) {
	t.Helper()
	runner := &harnesstest.Runner{}
	e := newCacheEnvWith(t, true, func(o *harness.ManagerOptions) {
		o.Runner, o.EventFillDelay = runner, 20*time.Millisecond
	})
	old := time.Now().Add(-time.Hour)
	for _, id := range []string{"s1", "s2"} {
		storeRunning(t, e.store, harness.Codex, id, "lbx-"+id)
		e.cache.Set(tenant, harness.Codex, id, old, ev("u1", "hi"))
		e.disc.SetSession(harness.Codex, id, time.Now(), ev("u1", "hi"), ev("a1", "reply"))
	}
	p1, p2 := runner.AddPTY("lbx-s1"), runner.AddPTY("lbx-s2")
	if err := e.mgr.Reconcile(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	return e, p1, p2
}

// TestFillAfterTerminalEnd writes the stale copy of a session after its
// terminal ended or detached, with one read and no wake.
func TestFillAfterTerminalEnd(t *testing.T) {
	e, p1, p2 := fillEnv(t)
	p1.Exit(0)
	eventually(t, "the copy after the end", func() bool {
		keys, _ := e.cache.Keys(tenant, harness.Codex, "s1")
		return slices.Equal(keys, []string{"u1", "a1"})
	})
	p2.Lose()
	eventually(t, "the copy after the detach", func() bool {
		keys, _ := e.cache.Keys(tenant, harness.Codex, "s2")
		return slices.Equal(keys, []string{"u1", "a1"})
	})
	time.Sleep(60 * time.Millisecond)
	if e.disc.Reads("codex:s1") != 1 || e.disc.Reads("codex:s2") != 1 || e.disc.Wakes() != 0 {
		t.Fatalf("reads s1 %d s2 %d, wakes %d", e.disc.Reads("codex:s1"), e.disc.Reads("codex:s2"), e.disc.Wakes())
	}
}

// TestFillNotWhileTheMachineDoesNotRun neither starts nor continues a fill
// when the machine does not run.
func TestFillNotWhileTheMachineDoesNotRun(t *testing.T) {
	e, p1, _ := fillEnv(t)
	e.disc.Pause()
	calls := e.disc.GuestCalls()
	p1.Lose()
	time.Sleep(100 * time.Millisecond)
	if n := e.disc.GuestCalls(); n != calls {
		t.Fatalf("a fill started while the machine did not run: %d guest calls", n-calls)
	}

	// The machine pauses during the first read: the second session is not
	// read, in the same fill or in a later one.
	e2, q1, q2 := fillEnv(t)
	e2.disc.OnRead = func(string) { e2.disc.Pause() }
	q1.Exit(0)
	q2.Exit(0)
	reads := func() int { return e2.disc.Reads("codex:s1") + e2.disc.Reads("codex:s2") }
	eventually(t, "the first read", func() bool { return reads() == 1 })
	time.Sleep(100 * time.Millisecond)
	if reads() != 1 || e2.disc.Wakes() != 0 {
		t.Fatalf("the fill continued after the pause: reads %d, wakes %d", reads(), e2.disc.Wakes())
	}
}

// TestEventCachePrune removes, after a fresh discovery of all harnesses
// without errors, the copies of sessions that the discovery did not find
// and that are not running. A discovery error keeps every copy.
func TestEventCachePrune(t *testing.T) {
	ctx := context.Background()
	e := newCacheEnv(t, true, time.Hour)
	now := time.Now()
	e.disc.SetSession(harness.Codex, "found", now.Add(-time.Hour), ev("u1", "a"))
	e.cache.Set(tenant, harness.Codex, "found", now, ev("u1", "a"))
	e.cache.Set(tenant, harness.Codex, "gone", now, ev("u1", "b"))
	e.cache.Set(tenant, harness.ClaudeCode, "running", now, ev("u1", "c"))
	storeRunning(t, e.store, harness.ClaudeCode, "running", "lbx-r")

	e.disc.DiscoverErrs = map[string]error{harness.OpenCode: errors.New("bad file")}
	if _, err := e.mgr.ListSessions(ctx, tenant, harness.Filter{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, ok := e.cache.Keys(tenant, harness.Codex, "gone"); !ok {
		t.Fatal("a discovery with an error removed a copy")
	}

	e.disc.DiscoverErrs = nil
	if _, err := e.mgr.ListSessions(ctx, tenant, harness.Filter{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the prune", func() bool {
		if _, err := e.mgr.ListSessions(ctx, tenant, harness.Filter{}); err != nil {
			t.Fatal(err)
		}
		_, ok := e.cache.Keys(tenant, harness.Codex, "gone")
		return !ok
	})
	if _, ok := e.cache.Keys(tenant, harness.Codex, "found"); !ok {
		t.Fatal("the prune removed the copy of a found session")
	}
	if _, ok := e.cache.Keys(tenant, harness.ClaudeCode, "running"); !ok {
		t.Fatal("the prune removed the copy of a running session")
	}
}
