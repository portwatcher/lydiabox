package vmm

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// remoteDriver is a fake remote machine that the provider can pause.
type remoteDriver struct {
	fakeDriver
	probe    time.Duration
	provider time.Duration
	observe  func(string, State)
}

func (d *remoteDriver) Capabilities() Capabilities {
	return Capabilities{Remote: true, SelfPause: true, ReadyProbeTimeout: d.probe, ProviderTimeout: d.provider}
}

func (d *remoteDriver) WatchState(fn func(string, State)) { d.observe = fn }

func TestRemoteBootingMachineIsAdoptedNotCreated(t *testing.T) {
	d := &remoteDriver{}
	d.set(StateBooting)
	ready := 0
	c, err := NewController(Options{
		Driver: d, Logger: slog.New(slog.DiscardHandler),
		Spec: func(context.Context, string) (Spec, error) { return Spec{CPUs: 1}, nil },
		Ready: func(context.Context, string) error {
			ready++
			if ready < 3 {
				return ErrUnavailable
			}
			d.set(StateRunning)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if st := c.State("t1"); st != StateBooting {
		t.Fatalf("refresh of a remote booting machine: %s", st)
	}
	if err := c.EnsureRunning(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); calls != "" {
		t.Fatalf("driver calls %q, want none (adopt the boot)", calls)
	}
	if c.State("t1") != StateRunning {
		t.Fatalf("state %s", c.State("t1"))
	}
}

func TestLocalBootingMachineIsMadeAgain(t *testing.T) {
	d := &fakeDriver{}
	d.set(StateBooting)
	c := newCtl(t, d, 0, nil)
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if st := c.State("t1"); st != StateStopped {
		t.Fatalf("refresh of a local booting VM: %s", st)
	}
	if err := c.EnsureRunning(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); calls != "create,boot" {
		t.Fatalf("calls %q", calls)
	}
}

func TestObservedSelfPauseIsResumedOnNextUse(t *testing.T) {
	d := &remoteDriver{probe: 50 * time.Millisecond}
	var probes []time.Duration
	c, err := NewController(Options{
		Driver: d, Logger: slog.New(slog.DiscardHandler), IdleTimeout: time.Hour,
		Spec: func(context.Context, string) (Spec, error) { return Spec{CPUs: 1}, nil },
		Ready: func(ctx context.Context, _ string) error {
			dl, _ := ctx.Deadline()
			probes = append(probes, time.Until(dl))
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.observe == nil {
		t.Fatal("the controller did not watch the driver state")
	}
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 || probes[0] > 50*time.Millisecond {
		t.Fatalf("readiness probe bounds %v, want at most 50ms", probes)
	}

	// The provider pauses the idle machine by itself.
	d.set(StatePaused)
	d.observe("t1", StatePaused)
	if st := c.Status("t1"); st.State != string(StatePaused) {
		t.Fatalf("after an observed pause: %+v", st)
	}
	if err := c.EnsureRunning(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); calls != "create,boot,resume" {
		t.Fatalf("calls %q", calls)
	}

	// An observed resume (a request woke the machine) needs no driver call.
	d.set(StatePaused)
	d.observe("t1", StatePaused)
	d.set(StateRunning)
	d.observe("t1", StateRunning)
	if err := c.EnsureRunning(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); calls != "create,boot,resume" {
		t.Fatalf("calls after an observed resume %q", calls)
	}
}

func TestObserveIsIgnoredDuringATransition(t *testing.T) {
	d := &remoteDriver{}
	d.bootDelay = 200 * time.Millisecond
	c, err := NewController(Options{
		Driver: d, Logger: slog.New(slog.DiscardHandler),
		Spec: func(context.Context, string) (Spec, error) { return Spec{CPUs: 1}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.EnsureRunning(context.Background(), "t1") }()
	time.Sleep(50 * time.Millisecond)
	d.observe("t1", StateStopped)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := c.State("t1"); st != StateRunning {
		t.Fatalf("an observation during the boot overrode it: %s", st)
	}
}

func TestCloseLeavesARemoteMachineRunning(t *testing.T) {
	d := &remoteDriver{}
	d.set(StateRunning)
	c, err := NewController(Options{
		Driver: d, Logger: slog.New(slog.DiscardHandler), IdleTimeout: time.Hour,
		Spec: func(context.Context, string) (Spec, error) { return Spec{CPUs: 1}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); calls != "" {
		t.Fatalf("driver calls %q at close, want none: the next host process adopts the machine", calls)
	}
}
