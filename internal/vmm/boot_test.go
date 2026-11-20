package vmm

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// slowRemote is a remote machine whose resume takes resumeDelay. The
// provider continues the resume when the caller stops its wait: the
// machine then runs after resumeDelay.
type slowRemote struct {
	remoteDriver
	resumeDelay time.Duration
}

func (d *slowRemote) Resume(ctx context.Context, _ string) error {
	d.record("resume")
	go func() {
		time.Sleep(d.resumeDelay)
		d.set(StateRunning)
	}()
	select {
	case <-time.After(d.resumeDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newRemoteCtl(t *testing.T, d Driver, boot time.Duration, ready func(context.Context, string) error) *Controller {
	t.Helper()
	if ready == nil {
		ready = func(context.Context, string) error { return nil }
	}
	c, err := NewController(Options{
		Driver: d, BootTimeout: boot, Logger: slog.New(slog.DiscardHandler),
		Spec:  func(context.Context, string) (Spec, error) { return Spec{CPUs: 1}, nil },
		Ready: ready,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A resume that takes longer than BootTimeout (the old bound of the full
// boot) works within the provider bound, and the state stays BOOTING.
func TestRemoteResumeSlowerThanBootTimeout(t *testing.T) {
	d := &slowRemote{resumeDelay: 400 * time.Millisecond}
	d.provider = 5 * time.Second
	d.set(StatePaused)
	c := newRemoteCtl(t, d, 100*time.Millisecond, nil)
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.EnsureRunning(context.Background(), "t1") }()
	time.Sleep(200 * time.Millisecond)
	if st := c.Status("t1"); st.State != string(StateBooting) || st.LastError != "" {
		t.Fatalf("status during the resume: %+v", st)
	}
	if err := <-done; err != nil {
		t.Fatalf("slow resume: %v", err)
	}
	if st := c.Status("t1"); st.State != string(StateRunning) || st.LastError != "" {
		t.Fatalf("status after the resume: %+v", st)
	}
}

// At the provider bound the error is clear, the machine is not stopped,
// and an observed run later clears the error.
func TestRemoteResumeAtProviderBound(t *testing.T) {
	d := &slowRemote{resumeDelay: time.Second}
	d.provider = 100 * time.Millisecond
	d.set(StatePaused)
	c := newRemoteCtl(t, d, time.Second, nil)
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	err := c.EnsureRunning(context.Background(), "t1")
	var pt *ProviderTimeoutError
	if !errors.As(err, &pt) || pt.Action != "resume" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error %v, want a ProviderTimeoutError", err)
	}
	if want := "the VM provider did not resume the machine in 100ms"; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("error text %q, want the prefix %q", err, want)
	}
	if st := c.Status("t1"); st.State != string(StatePaused) || st.LastError != err.Error() {
		t.Fatalf("status at the bound: %+v", st)
	}
	if calls := d.Calls(); calls != "resume" {
		t.Fatalf("calls %q, want only the resume (no stop)", calls)
	}
	d.observe("t1", StateRunning)
	if st := c.Status("t1"); st.State != string(StateRunning) || st.LastError != "" {
		t.Fatalf("status after an observed run: %+v", st)
	}
}

// A remote machine whose guest does not answer after the resume is paused
// (it keeps its processes), never stopped. A local VM is still stopped.
func TestFailedReadyPausesARemoteMachine(t *testing.T) {
	notReady := func(context.Context, string) error { return ErrUnavailable }
	d := &remoteDriver{provider: time.Second}
	d.set(StatePaused)
	c := newRemoteCtl(t, d, 100*time.Millisecond, notReady)
	if err := c.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRunning(context.Background(), "t1"); err == nil {
		t.Fatal("a boot without a ready guest worked")
	}
	if calls := d.Calls(); calls != "resume,pause" {
		t.Fatalf("remote calls %q, want resume,pause", calls)
	}
	if st := c.State("t1"); st != StatePaused {
		t.Fatalf("remote state %s", st)
	}

	local := &fakeDriver{}
	local.set(StatePaused)
	lc := newRemoteCtl(t, local, 100*time.Millisecond, notReady)
	if err := lc.Refresh(context.Background(), "t1"); err != nil {
		t.Fatal(err)
	}
	if err := lc.EnsureRunning(context.Background(), "t1"); err == nil {
		t.Fatal("a local boot without a ready guest worked")
	}
	if calls := local.Calls(); calls != "resume,shutdown-force" {
		t.Fatalf("local calls %q", calls)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Minute: "10 minutes", time.Minute: "1 minute", 90 * time.Second: "1m30s", 300 * time.Millisecond: "300ms",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
