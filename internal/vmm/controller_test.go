package vmm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDriver struct {
	mu        sync.Mutex
	state     State
	calls     []string
	bootDelay time.Duration
	failBoot  error
}

func (d *fakeDriver) record(c string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, c)
}

func (d *fakeDriver) Calls() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, ",")
}

func (d *fakeDriver) count(c string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, x := range d.calls {
		if x == c {
			n++
		}
	}
	return n
}

func (d *fakeDriver) set(s State) {
	d.mu.Lock()
	d.state = s
	d.mu.Unlock()
}

func (d *fakeDriver) Name() string { return "fake" }
func (d *fakeDriver) Create(context.Context, string, Spec) error {
	d.record("create")
	return nil
}
func (d *fakeDriver) Boot(context.Context, string) error {
	d.record("boot")
	time.Sleep(d.bootDelay)
	if d.failBoot != nil {
		return d.failBoot
	}
	d.set(StateRunning)
	return nil
}
func (d *fakeDriver) Pause(context.Context, string) error {
	d.record("pause")
	d.set(StatePaused)
	return nil
}
func (d *fakeDriver) Resume(context.Context, string) error {
	d.record("resume")
	d.set(StateRunning)
	return nil
}
func (d *fakeDriver) Shutdown(_ context.Context, _ string, force bool) error {
	if force {
		d.record("shutdown-force")
	} else {
		d.record("shutdown")
	}
	d.set(StateStopped)
	return nil
}
func (d *fakeDriver) State(context.Context, string) (State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == "" {
		return StateStopped, nil
	}
	return d.state, nil
}
func (d *fakeDriver) DialGuest(context.Context, string, uint32) (net.Conn, error) {
	return nil, errors.New("no guest")
}
func (d *fakeDriver) ListenGuestPort(context.Context, string, uint32) (net.Listener, error) {
	return nil, errors.New("no guest")
}

func newCtl(t *testing.T, d *fakeDriver, idle time.Duration, flush func(context.Context, string) error) *Controller {
	t.Helper()
	c, err := NewController(Options{
		Driver: d, IdleTimeout: idle, CPUs: 4, MemMB: 8192, Logger: slog.New(slog.DiscardHandler),
		Spec:  func(context.Context, string) (Spec, error) { return Spec{CPUs: 4}, nil },
		Ready: func(context.Context, string) error { return nil },
		Flush: flush,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEnsureRunningIsSingleflight(t *testing.T) {
	d := &fakeDriver{bootDelay: 50 * time.Millisecond}
	c := newCtl(t, d, 0, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.EnsureRunning(context.Background(), "default")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if d.count("create") != 1 || d.count("boot") != 1 {
		t.Fatalf("calls %s", d.Calls())
	}
	st := c.Status("default")
	if st.State != "RUNNING" || st.BootedAt.IsZero() || st.Driver != "fake" || st.CPUs != 4 {
		t.Fatalf("status %+v", st)
	}
	// A canceled caller does not cancel the shared boot.
	if err := c.EnsureRunning(context.Background(), "default"); err != nil || d.count("boot") != 1 {
		t.Fatalf("second ensure: %v %s", err, d.Calls())
	}
}

func TestBootFailureIsReportedAndCleanedUp(t *testing.T) {
	d := &fakeDriver{failBoot: errors.New("kvm busy")}
	c := newCtl(t, d, 0, nil)
	err := c.EnsureRunning(context.Background(), "default")
	if err == nil || !strings.Contains(err.Error(), "kvm busy") {
		t.Fatalf("err %v", err)
	}
	st := c.Status("default")
	if st.State != "STOPPED" || !strings.Contains(st.LastError, "kvm busy") || d.count("shutdown-force") != 1 {
		t.Fatalf("status %+v calls %s", st, d.Calls())
	}
}

func TestIdleTimerHibernatesWithoutLeases(t *testing.T) {
	d := &fakeDriver{}
	var flushed sync.WaitGroup
	flushed.Add(1)
	var once sync.Once
	c := newCtl(t, d, 80*time.Millisecond, func(context.Context, string) error {
		once.Do(flushed.Done)
		return nil
	})
	release := c.AcquireLease("default", LeaseDesktop)
	if err := c.EnsureRunning(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if c.State("default") != StateRunning {
		t.Fatal("hibernated while a lease was held")
	}
	st := c.Status("default")
	if st.Leases["desktop"] != 1 || st.Leases["pty"] != 0 || !st.IdleSince.IsZero() || st.IdleTimeoutSeconds != 0 {
		t.Fatalf("status %+v", st)
	}
	release()
	release() // idempotent
	deadline := time.Now().Add(5 * time.Second)
	for c.State("default") != StatePaused {
		if time.Now().After(deadline) {
			t.Fatalf("not hibernated: %s", d.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
	flushed.Wait()
	if !strings.HasSuffix(d.Calls(), "pause") {
		t.Fatalf("calls %s", d.Calls())
	}
	// Waking a paused VM resumes it.
	if err := c.EnsureRunning(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if d.count("resume") != 1 || c.State("default") != StateRunning {
		t.Fatalf("calls %s", d.Calls())
	}
}

func TestPowerTransitionsAndConflicts(t *testing.T) {
	d := &fakeDriver{}
	var flushes int
	var mu sync.Mutex
	c := newCtl(t, d, 0, func(context.Context, string) error {
		mu.Lock()
		flushes++
		mu.Unlock()
		return nil
	})
	ctx := context.Background()
	if err := c.Hibernate(ctx, "default"); !errors.Is(err, ErrConflict) {
		t.Fatalf("hibernate while stopped: %v", err)
	}
	if err := c.Stop(ctx, "default", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stop while stopped: %v", err)
	}
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Hibernate(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	if got := d.Calls(); got != "create,boot,pause,resume,shutdown" {
		t.Fatalf("calls %s", got)
	}
	mu.Lock()
	if flushes != 2 {
		t.Fatalf("flushes %d", flushes)
	}
	mu.Unlock()
	st := c.Status("default")
	if st.State != "STOPPED" || !st.BootedAt.IsZero() {
		t.Fatalf("status %+v", st)
	}
	b, _ := json.Marshal(st)
	for _, key := range []string{`"state":"STOPPED"`, `"driver":"fake"`, `"memMb":8192`, `"leases":{"desktop":0,"pty":0}`, `"idleTimeoutSeconds":0`, `"idleSince":`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("missing %s in %s", key, b)
		}
	}
	if strings.Contains(string(b), "gpu") || strings.Contains(string(b), "lastError") {
		t.Fatalf("empty optional fields present: %s", b)
	}

	// A failed flush blocks a graceful stop but not a forced one.
	c2 := newCtl(t, &fakeDriver{}, 0, func(context.Context, string) error { return errors.New("guest gone") })
	if err := c2.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c2.Stop(ctx, "default", false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("graceful stop with failed flush: %v", err)
	}
	if err := c2.Stop(ctx, "default", true); err != nil {
		t.Fatal(err)
	}
	if vs, _ := c2.VMStatus(ctx, "default"); vs.State != "STOPPED" || vs.Driver != "fake" {
		t.Fatalf("VMStatus %+v", vs)
	}
}

func TestRefreshAdoptsRunningVM(t *testing.T) {
	d := &fakeDriver{state: StateRunning}
	c := newCtl(t, d, 0, nil)
	if err := c.Refresh(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if c.State("default") != StateRunning {
		t.Fatal("not adopted")
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.count("shutdown-force") != 1 {
		t.Fatalf("calls %s", d.Calls())
	}
}

func TestUserStopHoldsUntilStart(t *testing.T) {
	d := &fakeDriver{}
	c := newCtl(t, d, 0, nil)
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	// A wake on demand (desktop, terminal, files) does not start it.
	if err := c.EnsureRunning(ctx, "default"); !errors.Is(err, ErrStopped) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("EnsureRunning after a user stop: %v", err)
	}
	if !c.Held("default") || c.State("default") != StateStopped || d.count("boot") != 1 {
		t.Fatalf("held %v state %s calls %s", c.Held("default"), c.State("default"), d.Calls())
	}
	// An explicit start lifts the hold, again and again.
	for i := 0; i < 2; i++ {
		if err := c.Start(ctx, "default"); err != nil {
			t.Fatal(err)
		}
		if c.Held("default") || c.State("default") != StateRunning {
			t.Fatalf("after start: held %v state %s", c.Held("default"), c.State("default"))
		}
		if err := c.EnsureRunning(ctx, "default"); err != nil {
			t.Fatal(err)
		}
		if err := c.Stop(ctx, "default", false); err != nil {
			t.Fatal(err)
		}
		if err := c.EnsureRunning(ctx, "default"); !errors.Is(err, ErrStopped) {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	if d.count("boot") != 3 {
		t.Fatalf("calls %s", d.Calls())
	}
}

func TestIdlePauseIsNotAUserStop(t *testing.T) {
	d := &fakeDriver{}
	c := newCtl(t, d, 0, nil)
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Hibernate(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureRunning(ctx, "default"); err != nil || c.Held("default") {
		t.Fatalf("wake of a paused VM: %v held %v", err, c.Held("default"))
	}
	// A failed stop does not hold the VM.
	c2 := newCtl(t, &fakeDriver{}, 0, func(context.Context, string) error { return errors.New("guest gone") })
	if err := c2.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c2.Stop(ctx, "default", false); err == nil || c2.Held("default") {
		t.Fatalf("failed stop: %v held %v", err, c2.Held("default"))
	}
}

// A wake asked for before a Stop, which runs after the stop, must not
// start the VM again.
func TestWakeQueuedBehindAStopDoesNotBoot(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	d := &fakeDriver{}
	var once sync.Once
	c := newCtl(t, d, 0, func(context.Context, string) error {
		once.Do(func() { close(entered); <-release })
		return nil
	})
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop(ctx, "default", false) }()
	<-entered
	// The stop holds the VM from its start: a wake fails at once.
	if err := c.EnsureRunning(ctx, "default"); !errors.Is(err, ErrStopped) {
		t.Fatalf("wake during the stop: %v", err)
	}
	close(release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if d.count("boot") != 1 || c.State("default") != StateStopped {
		t.Fatalf("calls %s state %s", d.Calls(), c.State("default"))
	}
}
