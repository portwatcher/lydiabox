package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
	"github.com/lydiabox/lydiabox/internal/store"
)

// storeProvisional stores a provisional session (no transcript yet) whose
// terminal ptyID runs in the guest, as a pause leaves it.
func storeProvisional(t *testing.T, st *harnesstest.Store, h, nativeID, ptyID string) {
	t.Helper()
	x, _ := json.Marshal(map[string]any{"ptyId": ptyID, "provisional": true})
	err := st.UpsertSessionMeta(context.Background(), tenant, store.SessionMeta{Harness: h, SessionID: nativeID,
		WorkspaceDir: "/workspace", Status: harness.StatusRunning, Extra: x})
	if err != nil {
		t.Fatal(err)
	}
}

// drain applies the updates that come in d.
func (c *client) drain(sub *harness.EventSub, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case u, ok := <-sub.C:
			if !ok {
				return
			}
			c.apply(u)
		case <-deadline:
			return
		}
	}
}

// TestProvisionalSessionDoesNotWaitForTheMachine opens a provisional session
// that is not live while the machine is paused: the first update is synced
// with no events, the wake is asked for once in the background, and the
// session is live after Reconcile attached its terminal again.
func TestProvisionalSessionDoesNotWaitForTheMachine(t *testing.T) {
	runner := &harnesstest.Runner{}
	e := newCacheEnvWith(t, false, func(o *harness.ManagerOptions) { o.Runner = runner })
	id := "antigravity:new-3c6b1fff0eae14f4"
	storeProvisional(t, e.store, harness.Antigravity, "new-3c6b1fff0eae14f4", "lbx-agy")
	runner.AddPTY("lbx-agy")

	sub, first, done, err := e.mgr.SubscribeEvents(tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	if !first.Status.Synced || first.Status.Live || len(first.Events) != 0 || first.Status.Error != "" {
		t.Fatalf("first status = %+v, events %d", first.Status, len(first.Events))
	}
	// The wake waits for the machine; the client does not.
	eventually(t, "the wake", func() bool { return e.disc.Wakes() == 1 })
	c.drain(sub, 100*time.Millisecond)
	if !c.status.Synced || c.status.Error != "" || c.status.Live || len(c.order) != 0 {
		t.Fatalf("status while the machine starts = %+v", c.status)
	}
	if n := e.disc.Reads(id); n != 0 {
		t.Fatalf("the hub read the provisional ID %d times", n)
	}

	e.disc.Resume()
	if err := e.mgr.Reconcile(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	c.follow(t, sub, "the live terminal", func() bool { return c.status.Live })
	if !c.status.Synced || c.status.State == harness.StateOffline || c.status.SessionID != id {
		t.Fatalf("live status = %+v", c.status)
	}
	if n := e.disc.Wakes(); n != 1 {
		t.Fatalf("wakes = %d, want 1", n)
	}
}

// TestProvisionalSessionStoppedMachine does not start a machine that the
// user stopped and shows no error for the session.
func TestProvisionalSessionStoppedMachine(t *testing.T) {
	e := newCacheEnvWith(t, false, func(*harness.ManagerOptions) {})
	e.disc.WakeErr = errors.New("the VM was stopped")
	id := "antigravity:new-1"
	storeProvisional(t, e.store, harness.Antigravity, "new-1", "lbx-agy")
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, id)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	eventually(t, "the wake", func() bool { return e.disc.Wakes() == 1 })
	c.drain(sub, 100*time.Millisecond)
	if !c.status.Synced || c.status.Error != "" || c.status.Live {
		t.Fatalf("status = %+v", c.status)
	}
	if e.disc.Serving(tenant) || e.disc.Wakes() != 1 || e.disc.Reads(id) != 0 {
		t.Fatalf("serving %v, wakes %d, reads %d", e.disc.Serving(tenant), e.disc.Wakes(), e.disc.Reads(id))
	}
}

// TestGuestDiscoveryWakeForEvents uses the wake of the event read: a files
// lease that ends after the wake, and no start of a stopped machine.
func TestGuestDiscoveryWakeForEvents(t *testing.T) {
	p := newProviderEnv(t)
	ctx := context.Background()
	if err := p.disc.WakeForEvents(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if p.guest.Waker.Wakes() != 1 || p.guest.Waker.Leases("files") != 0 {
		t.Fatalf("wakes %d, files leases %d", p.guest.Waker.Wakes(), p.guest.Waker.Leases("files"))
	}
	p.guest.Waker.Set(0, agentfs.ErrStopped)
	if err := p.disc.WakeForEvents(ctx, tenant); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("wake of a stopped machine = %v", err)
	}
}
