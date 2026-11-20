package harness_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

// cacheEnv is a manager in the provider storage mode with a fake guest
// discovery and a fake event cache.
type cacheEnv struct {
	disc  *harnesstest.EventDiscovery
	cache *harnesstest.EventCache
	store *harnesstest.Store
	mgr   *harness.Manager
}

func newCacheEnv(t *testing.T, running bool, delay time.Duration) *cacheEnv {
	t.Helper()
	e := &cacheEnv{disc: harnesstest.NewEventDiscovery(running), cache: &harnesstest.EventCache{}, store: &harnesstest.Store{}}
	home := t.TempDir()
	mgr, err := harness.NewManager(harness.ManagerOptions{
		Adapters: adapters.All(), Files: localFiles(home, t.TempDir()), Discovery: e.disc, Store: e.store,
		Logger: quiet(), EventPoll: 10 * time.Millisecond, EventCache: e.cache, EventCopyDelay: delay,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mgr = mgr
	return e
}

func ev(key, text string) harness.Event {
	return harness.Event{Key: key, Kind: harness.EventAssistant, Text: text}
}

// client applies updates the way the web client does (chatState.ts).
type client struct {
	order  []string
	byKey  map[string]harness.Event
	status harness.EventStatus
	resets int
}

func (c *client) apply(u harness.EventUpdate) {
	if u.Reset || c.byKey == nil {
		if u.Reset {
			c.resets++
		}
		c.order, c.byKey = nil, map[string]harness.Event{}
	}
	for _, e := range u.Events {
		if _, ok := c.byKey[e.Key]; !ok {
			c.order = append(c.order, e.Key)
		}
		c.byKey[e.Key] = e
	}
	if u.Status != nil {
		c.status = *u.Status
	}
}

// follow applies the updates of sub until cond holds.
func (c *client) follow(t *testing.T, sub *harness.EventSub, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case u, ok := <-sub.C:
			if !ok {
				t.Fatalf("stream closed while waiting for %s", what)
			}
			c.apply(u)
		case <-deadline:
			t.Fatalf("timed out waiting for %s (client %v, status %+v)", what, c.order, c.status)
		}
	}
}

// TestEventCachePreload shows the stored copy while the read that wakes the
// machine waits, then gives the client exactly the guest events.
func TestEventCachePreload(t *testing.T) {
	e := newCacheEnv(t, false, time.Hour)
	old := time.Now().Add(-time.Hour)
	e.cache.Set(tenant, harness.Codex, "s1", old, ev("u1", "hi"), ev("a1", "one"), ev("a2", "two"))
	e.disc.SetSession(harness.Codex, "s1", old, ev("u1", "hi again"), ev("a1", "one"), ev("a3", "three"))

	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	if !first.Reset || !slices.Equal(c.order, []string{"u1", "a1", "a2"}) || !first.Status.Cached || first.Status.Synced {
		t.Fatalf("first update = %v, status %+v", c.order, first.Status)
	}
	// The first read asks to wake the machine and waits for it.
	eventually(t, "the wake", func() bool { return e.disc.Wakes() > 0 })
	select {
	case u := <-sub.C:
		t.Fatalf("an update came before the machine ran: %+v", u)
	case <-time.After(50 * time.Millisecond):
	}

	e.disc.Resume()
	c.follow(t, sub, "the guest events", func() bool { return c.status.Synced && !c.status.Cached })
	if !slices.Equal(c.order, []string{"u1", "a1", "a3"}) || c.byKey["u1"].Text != "hi again" || c.resets != 2 {
		t.Fatalf("client = %v (u1 %q, resets %d)", c.order, c.byKey["u1"].Text, c.resets)
	}
}

// TestEventCachePreloadAppend updates the copy in place when its order is
// the start of the guest order.
func TestEventCachePreloadAppend(t *testing.T) {
	e := newCacheEnv(t, true, time.Hour)
	old := time.Now().Add(-time.Hour)
	e.cache.Set(tenant, harness.Codex, "s1", old, ev("u1", "hi"), ev("a1", "part"))
	e.disc.SetSession(harness.Codex, "s1", old, ev("u1", "hi"), ev("a1", "part and more"), ev("a2", "new"))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	var updates []harness.EventUpdate
	deadline := time.After(5 * time.Second)
	for !c.status.Synced {
		select {
		case u := <-sub.C:
			updates = append(updates, u)
			c.apply(u)
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if c.resets != 1 || !slices.Equal(c.order, []string{"u1", "a1", "a2"}) || c.byKey["a1"].Text != "part and more" {
		t.Fatalf("client = %v, resets %d", c.order, c.resets)
	}
	var sent []string
	for _, u := range updates {
		for _, x := range u.Events {
			sent = append(sent, x.Key)
		}
	}
	if !slices.Equal(sent, []string{"a1", "a2"}) {
		t.Fatalf("sent %v, want only the changed and new events", sent)
	}
}

// TestEventCacheReadFailKeepsCopy keeps the copy on the screen when the
// read fails.
func TestEventCacheReadFailKeepsCopy(t *testing.T) {
	e := newCacheEnv(t, false, time.Hour)
	e.disc.WakeErr = errors.New("the VM is stopped")
	e.cache.Set(tenant, harness.Codex, "s1", time.Now(), ev("u1", "hi"))
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	var c client
	c.apply(first)
	c.follow(t, sub, "the read error", func() bool { return c.status.Error != "" })
	if !slices.Equal(c.order, []string{"u1"}) || !c.status.Cached || c.status.Synced {
		t.Fatalf("client = %v, status %+v", c.order, c.status)
	}
}

// TestEventCacheWrites writes the copy after changes (debounced) and once
// more when the hub ends.
func TestEventCacheWrites(t *testing.T) {
	e := newCacheEnv(t, true, 20*time.Millisecond)
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	var c client
	c.apply(first)
	eventually(t, "the first write", func() bool {
		keys, ok := e.cache.Keys(tenant, harness.Codex, "s1")
		return ok && slices.Equal(keys, []string{"u1"})
	})
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"), ev("a1", "reply"))
	c.follow(t, sub, "the reply", func() bool { return len(c.order) == 2 })
	eventually(t, "the second write", func() bool {
		keys, _ := e.cache.Keys(tenant, harness.Codex, "s1")
		return slices.Equal(keys, []string{"u1", "a1"})
	})
	puts := e.cache.Puts()
	time.Sleep(80 * time.Millisecond)
	if e.cache.Puts() != puts {
		t.Fatalf("the copy was written again without a change (%d -> %d writes)", puts, e.cache.Puts())
	}
	done()

	// With a long delay, the copy is written only when the hub ends.
	e2 := newCacheEnv(t, true, time.Hour)
	e2.disc.SetSession(harness.Codex, "s2", time.Now(), ev("u1", "hi"), ev("a1", "reply"))
	sub2, first2, done2, err := e2.mgr.SubscribeEvents(tenant, "codex:s2")
	if err != nil {
		t.Fatal(err)
	}
	var c2 client
	c2.apply(first2)
	c2.follow(t, sub2, "the events", func() bool { return c2.status.Synced })
	time.Sleep(50 * time.Millisecond)
	if e2.cache.Puts() != 0 {
		t.Fatal("the copy was written before the delay")
	}
	done2()
	eventually(t, "the write at the hub end", func() bool {
		keys, _ := e2.cache.Keys(tenant, harness.Codex, "s2")
		return slices.Equal(keys, []string{"u1", "a1"})
	})
}

// TestEventCacheDelete removes the copy with the session, and an open hub
// does not write it again.
func TestEventCacheDelete(t *testing.T) {
	ctx := context.Background()
	e := newCacheEnv(t, true, 10*time.Millisecond)
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"))
	e.disc.SetSession(harness.Codex, "s2", time.Now(), ev("u1", "hi"))
	e.cache.Set(tenant, harness.Codex, "s2", time.Now(), ev("u1", "hi"))
	sub, first, done, err := e.mgr.SubscribeEvents(tenant, "codex:s1")
	if err != nil {
		t.Fatal(err)
	}
	var c client
	c.apply(first)
	c.follow(t, sub, "the events", func() bool { return c.status.Synced })
	eventually(t, "the write", func() bool { _, ok := e.cache.Keys(tenant, harness.Codex, "s1"); return ok })

	if err := e.mgr.Delete(ctx, tenant, "codex:s1", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.cache.Keys(tenant, harness.Codex, "s1"); ok {
		t.Fatal("a dismissal kept the copy")
	}
	e.disc.SetSession(harness.Codex, "s1", time.Now(), ev("u1", "hi"), ev("a1", "more"))
	time.Sleep(60 * time.Millisecond)
	done()
	time.Sleep(60 * time.Millisecond)
	if _, ok := e.cache.Keys(tenant, harness.Codex, "s1"); ok {
		t.Fatal("the hub wrote the copy of a deleted session")
	}

	if err := e.mgr.Delete(ctx, tenant, "codex:s2", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.cache.Keys(tenant, harness.Codex, "s2"); ok {
		t.Fatal("a delete with purge kept the copy")
	}
}

// TestEventCacheFill reads only the stale sessions after a fresh
// discovery, newest first, without a wake.
func TestEventCacheFill(t *testing.T) {
	ctx := context.Background()
	e := newCacheEnv(t, true, time.Hour)
	now := time.Now()
	e.disc.SetSession(harness.Codex, "none", now.Add(-time.Hour), ev("u1", "a"))
	e.disc.SetSession(harness.Codex, "stale", now.Add(-30*time.Minute), ev("u1", "b"))
	e.cache.Set(tenant, harness.Codex, "stale", now.Add(-2*time.Hour), ev("old", "b"))
	e.disc.SetSession(harness.Codex, "current", now.Add(-3*time.Hour), ev("u1", "c"))
	e.cache.Set(tenant, harness.Codex, "current", now.Add(-time.Hour), ev("u1", "c"))
	e.disc.SetSession(harness.Codex, "young", now.Add(-time.Second), ev("u1", "d"))
	e.cache.Set(tenant, harness.Codex, "young", now.Add(-5*time.Second), ev("old", "d"))
	var order []string
	readDone := make(chan string, 8)
	e.disc.OnRead = func(source string) { readDone <- source }

	if _, err := e.mgr.ListSessions(ctx, tenant, harness.Filter{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case s := <-readDone:
			order = append(order, s)
		case <-time.After(5 * time.Second):
			t.Fatalf("fill reads = %v", order)
		}
	}
	if !slices.Equal(order, []string{"codex:stale", "codex:none"}) {
		t.Fatalf("fill order = %v", order)
	}
	eventually(t, "the copies", func() bool {
		a, _ := e.cache.Keys(tenant, harness.Codex, "none")
		b, _ := e.cache.Keys(tenant, harness.Codex, "stale")
		return slices.Equal(a, []string{"u1"}) && slices.Equal(b, []string{"u1"})
	})
	time.Sleep(30 * time.Millisecond)
	if e.disc.Reads("codex:current") != 0 || e.disc.Reads("codex:young") != 0 || e.disc.Wakes() != 0 {
		t.Fatalf("reads current %d young %d, wakes %d", e.disc.Reads("codex:current"), e.disc.Reads("codex:young"), e.disc.Wakes())
	}
}

// TestEventCacheFillStops stops the fill when the machine does not run.
func TestEventCacheFillStops(t *testing.T) {
	e := newCacheEnv(t, true, time.Hour)
	now := time.Now()
	for i, id := range []string{"x", "y", "z"} {
		e.disc.SetSession(harness.Codex, id, now.Add(-time.Duration(i)*time.Minute), ev("u1", id))
	}
	e.disc.OnRead = func(string) { e.disc.Pause() }
	if _, err := e.mgr.ListSessions(context.Background(), tenant, harness.Filter{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first copy", func() bool { _, ok := e.cache.Keys(tenant, harness.Codex, "x"); return ok })
	time.Sleep(50 * time.Millisecond)
	if n := e.disc.Reads("codex:y") + e.disc.Reads("codex:z"); n != 0 || e.disc.Wakes() != 0 {
		t.Fatalf("the fill read %d sessions after the pause, wakes %d", n, e.disc.Wakes())
	}
}

// TestEventCacheBeforePause fills the copies through the guest while the
// controller does not serve the machine (a pause in progress), without a
// wake.
func TestEventCacheBeforePause(t *testing.T) {
	p := newProviderEnv(t)
	p.running.Store(false)
	cache := &harnesstest.EventCache{}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: p.guest.FS, Discovery: p.disc,
		Store: &harnesstest.Store{}, Logger: quiet(), EventCache: cache})
	if err != nil {
		t.Fatal(err)
	}
	mgr.FillEventCacheBeforePause(context.Background(), tenant)
	stamps, err := cache.ListSessionEventsStamps(context.Background(), tenant)
	if err != nil || len(stamps) == 0 {
		t.Fatalf("stamps = %+v, %v", stamps, err)
	}
	if p.guest.Waker.Wakes() != 0 {
		t.Fatal("the fill before a pause woke the machine")
	}
	// Without a cache nothing happens.
	off, _ := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: p.guest.FS, Discovery: p.disc,
		Store: &harnesstest.Store{}, Logger: quiet()})
	off.FillEventCacheBeforePause(context.Background(), tenant)
}
