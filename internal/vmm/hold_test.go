package vmm

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/store"
)

// holdEnv is a state database on disk that new controllers (new host
// processes) open again.
type holdEnv struct {
	t    *testing.T
	path string
	key  []byte
}

func newHoldEnv(t *testing.T) *holdEnv {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return &holdEnv{t: t, path: filepath.Join(t.TempDir(), "lydiabox.db"), key: key}
}

func (e *holdEnv) open() *store.Store {
	e.t.Helper()
	st, err := store.Open(context.Background(), e.path, e.key)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { st.Close() })
	return st
}

// controller is one host process: a controller over d with the hold in st,
// started as wireVM starts it (Refresh, then LoadHold).
func (e *holdEnv) controller(d *fakeDriver, st HoldStore, flush func(context.Context, string) error) *Controller {
	e.t.Helper()
	c, err := NewController(Options{
		Driver: d, Holds: st, Logger: slog.New(slog.DiscardHandler),
		Spec:  func(context.Context, string) (Spec, error) { return Spec{CPUs: 4}, nil },
		Ready: func(context.Context, string) error { return nil },
		Flush: flush,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Refresh(ctx, "default"); err != nil {
		e.t.Fatal(err)
	}
	if err := c.LoadHold(ctx, "default"); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func storedHold(t *testing.T, st *store.Store) bool {
	t.Helper()
	held, _, err := st.VMHold(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// A user stop holds the VM across a host restart; an explicit start in the
// new process clears the stored hold.
func TestUserStopHoldSurvivesAHostRestart(t *testing.T) {
	ctx := context.Background()
	e := newHoldEnv(t)
	d := &fakeDriver{}
	st := e.open()
	c := e.controller(d, st, nil)
	if err := c.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	if !storedHold(t, st) {
		t.Fatal("the stop did not store the hold")
	}
	st.Close()

	// A new host process on the same state database.
	st = e.open()
	c = e.controller(d, st, nil)
	if !c.Held("default") {
		t.Fatal("the new process did not load the hold")
	}
	if err := c.EnsureRunning(ctx, "default"); !errors.Is(err, ErrStopped) {
		t.Fatalf("wake on demand after a restart: %v", err)
	}
	if d.count("boot") != 1 {
		t.Fatalf("calls %s", d.Calls())
	}
	if err := c.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if c.State("default") != StateRunning || storedHold(t, st) {
		t.Fatalf("after start: state %s, stored hold %v", c.State("default"), storedHold(t, st))
	}
	st.Close()

	// After the start, a new process wakes the VM on demand.
	st = e.open()
	d.set(StateStopped)
	c = e.controller(d, st, nil)
	if c.Held("default") {
		t.Fatal("the start did not clear the stored hold")
	}
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
}

// A failed or refused stop restores the stored hold that was there before.
func TestFailedStopRestoresTheStoredHold(t *testing.T) {
	ctx := context.Background()
	e := newHoldEnv(t)
	st := e.open()
	c := e.controller(&fakeDriver{}, st, func(context.Context, string) error { return errors.New("guest gone") })
	if err := c.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "default", false); err == nil {
		t.Fatal("want a failed stop")
	}
	if c.Held("default") || storedHold(t, st) {
		t.Fatalf("failed stop: held %v, stored %v", c.Held("default"), storedHold(t, st))
	}
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatalf("a failed stop must not hold the VM: %v", err)
	}
	// A forced stop works; a second (refused) stop keeps the hold.
	if err := c.Stop(ctx, "default", true); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "default", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stop of a stopped VM: %v", err)
	}
	if !c.Held("default") || !storedHold(t, st) {
		t.Fatalf("refused stop: held %v, stored %v", c.Held("default"), storedHold(t, st))
	}
}

// A host shutdown (Close), Hibernate, and the idle hibernation are not
// user stops: the next process wakes the VM on demand.
func TestShutdownAndHibernateDoNotHold(t *testing.T) {
	ctx := context.Background()
	e := newHoldEnv(t)
	d := &fakeDriver{}
	st := e.open()
	c := e.controller(d, st, nil)
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Hibernate(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatalf("wake after Hibernate: %v", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Held("default") || storedHold(t, st) || d.count("shutdown-force") != 1 {
		t.Fatalf("close: held %v stored %v calls %s", c.Held("default"), storedHold(t, st), d.Calls())
	}
	c = e.controller(d, st, nil)
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatalf("wake after a host shutdown: %v", err)
	}

	idle, err := NewController(Options{Driver: d, Holds: st, IdleTimeout: 50 * time.Millisecond,
		Logger: slog.New(slog.DiscardHandler),
		Spec:   func(context.Context, string) (Spec, error) { return Spec{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := idle.Refresh(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := idle.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for idle.State("default") != StatePaused && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if idle.State("default") != StatePaused || idle.Held("default") || storedHold(t, st) {
		t.Fatalf("idle hibernation: state %s held %v stored %v", idle.State("default"), idle.Held("default"), storedHold(t, st))
	}
}

// A stored hold of a VM that runs (the stop did not finish) is dropped.
func TestStoredHoldOfARunningVMIsDropped(t *testing.T) {
	e := newHoldEnv(t)
	st := e.open()
	if err := st.SetVMHold(context.Background(), "default", true); err != nil {
		t.Fatal(err)
	}
	c := e.controller(&fakeDriver{state: StateRunning}, st, nil)
	if c.Held("default") || storedHold(t, st) {
		t.Fatal("the hold of a running VM was kept")
	}
}

type brokenHolds struct{}

func (brokenHolds) VMHold(context.Context, string) (bool, time.Time, error) {
	return false, time.Time{}, errors.New("disk I/O error")
}
func (brokenHolds) SetVMHold(context.Context, string, bool) error {
	return errors.New("disk I/O error")
}

// A state database that cannot be read or written does not make the VM
// impossible to start.
func TestBrokenHoldStoreDoesNotBlockAStart(t *testing.T) {
	ctx := context.Background()
	c, err := NewController(Options{Driver: &fakeDriver{}, Holds: brokenHolds{}, Logger: slog.New(slog.DiscardHandler),
		Spec: func(context.Context, string) (Spec, error) { return Spec{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.LoadHold(ctx, "default"); err == nil || c.Held("default") {
		t.Fatalf("LoadHold: %v held %v", err, c.Held("default"))
	}
	if err := c.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRunning(ctx, "default"); !errors.Is(err, ErrStopped) {
		t.Fatalf("the hold in memory must still apply: %v", err)
	}
	if err := c.Start(ctx, "default"); err != nil || c.State("default") != StateRunning {
		t.Fatalf("start: %v state %s", err, c.State("default"))
	}
}
