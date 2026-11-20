package vmm

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// slowPause is a local VM whose pause takes delay; the pause ends early
// when its context ends.
type slowPause struct {
	fakeDriver
	delay time.Duration
}

func (d *slowPause) Pause(ctx context.Context, _ string) error {
	d.record("pause")
	select {
	case <-time.After(d.delay):
		d.set(StatePaused)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newPauseCtl(t *testing.T, d Driver, boot time.Duration) *Controller {
	t.Helper()
	c, err := NewController(Options{
		Driver: d, BootTimeout: boot, Logger: slog.New(slog.DiscardHandler),
		Spec:  func(context.Context, string) (Spec, error) { return Spec{CPUs: 1}, nil },
		Ready: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRunning(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	return c
}

// A caller that goes away during the pause does not cancel it: the status
// shows the transition, then PAUSED without an error.
func TestHibernateContinuesWithoutTheCaller(t *testing.T) {
	d := &slowPause{delay: 300 * time.Millisecond}
	c := newPauseCtl(t, d, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if err := c.Hibernate(ctx, "t1"); !errors.Is(err, ErrPausing) {
		t.Fatalf("hibernate: %v, want ErrPausing", err)
	}
	if st := c.Status("t1"); st.State != string(StateRunning) || st.Transition != TransitionPausing || st.LastError != "" || c.Serving("t1") {
		t.Fatalf("status during the pause: %+v serving %v", st, c.Serving("t1"))
	}
	deadline := time.Now().Add(5 * time.Second)
	for c.State("t1") != StatePaused {
		if time.Now().After(deadline) {
			t.Fatalf("not paused: %+v", c.Status("t1"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st := c.Status("t1"); st.LastError != "" || st.Transition != "" {
		t.Fatalf("status after the pause: %+v", st)
	}
}

// A local pause that reaches its bound gives a clear error, and the VM
// keeps running.
func TestHibernateAtItsBound(t *testing.T) {
	d := &slowPause{delay: 5 * time.Second}
	c := newPauseCtl(t, d, 100*time.Millisecond)
	err := c.Hibernate(context.Background(), "t1")
	var pt *ProviderTimeoutError
	if !errors.As(err, &pt) || pt.Action != "pause" || err.Error() != "the VM did not pause in 100ms" {
		t.Fatalf("error %v", err)
	}
	if st := c.Status("t1"); st.State != string(StateRunning) || st.LastError != err.Error() || st.Transition != "" {
		t.Fatalf("status after the pause bound: %+v", st)
	}
}

// failPause is a remote machine whose pause fails while the machine
// still runs.
type failPause struct {
	remoteDriver
	failResume bool
}

func (d *failPause) Pause(context.Context, string) error {
	d.record("pause")
	return errors.New("the provider refused the suspend")
}

func (d *failPause) Resume(context.Context, string) error {
	d.record("resume")
	d.mu.Lock()
	fail := d.failResume
	d.mu.Unlock()
	if fail {
		return errors.New("no link")
	}
	return nil
}

// After a failed pause of a remote machine that runs, the controller
// attaches the guest link again (the driver's resume). When that fails
// too, the next wake attaches it before the VM is used.
func TestRemoteFailedPauseAttachesTheLinkAgain(t *testing.T) {
	d := &failPause{failResume: true}
	d.set(StateRunning)
	c := newRemoteCtl(t, d, time.Second, nil)
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Hibernate(context.Background(), "t1"); err == nil || !strings.Contains(err.Error(), "refused the suspend") {
		t.Fatalf("hibernate: %v", err)
	}
	if st := c.Status("t1"); st.State != string(StateRunning) || st.LastError == "" {
		t.Fatalf("status after the failed pause: %+v", st)
	}
	d.mu.Lock()
	d.failResume = false
	d.mu.Unlock()
	if err := c.EnsureRunning(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); calls != "pause,resume,resume" {
		t.Fatalf("calls %q, want pause,resume,resume", calls)
	}
	if st := c.Status("t1"); st.State != string(StateRunning) || st.LastError != "" {
		t.Fatalf("status after the wake: %+v", st)
	}
	// The link works now: the next wake does not call the driver.
	if err := c.EnsureRunning(context.Background(), "t1"); err != nil || d.Calls() != "pause,resume,resume" {
		t.Fatalf("second wake: %v, calls %q", err, d.Calls())
	}
}

// A boot clears the error of an earlier failed transition at its start:
// BOOTING never shows an old error.
func TestBootClearsAnOldError(t *testing.T) {
	d := &fakeDriver{failBoot: errors.New("no boot")}
	c := newCtl(t, d, 0, nil)
	if err := c.EnsureRunning(context.Background(), "t1"); err == nil {
		t.Fatal("the first boot worked")
	}
	if c.Status("t1").LastError == "" {
		t.Fatal("no error after a failed boot")
	}
	d.failBoot, d.bootDelay = nil, 300*time.Millisecond
	done := make(chan error, 1)
	go func() { done <- c.EnsureRunning(context.Background(), "t1") }()
	time.Sleep(100 * time.Millisecond)
	if st := c.Status("t1"); st.State != string(StateBooting) || st.LastError != "" {
		t.Fatalf("status during the next boot: %+v", st)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
