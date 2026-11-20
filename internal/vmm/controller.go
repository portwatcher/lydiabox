package vmm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/api"
)

// Lease kinds (docs/api.md section 7).
const (
	LeasePTY     = "pty"
	LeaseDesktop = "desktop"
)

// Default timeouts.
const (
	DefaultBootTimeout = 120 * time.Second
	FlushTimeout       = 30 * time.Second
	// DefaultReadyProbeTimeout bounds one guest readiness probe.
	DefaultReadyProbeTimeout = 5 * time.Second
)

// Options configures a Controller.
type Options struct {
	Driver Driver
	// Spec builds the VM spec for a tenant at boot time.
	Spec func(ctx context.Context, tenantID string) (Spec, error)
	// IdleTimeout hibernates the VM after this long without leases
	// (0 disables).
	IdleTimeout time.Duration
	// Ready reports whether the guest daemon answers; EnsureRunning polls it
	// after boot or resume.
	Ready func(ctx context.Context, tenantID string) error
	// Flush syncs guest filesystems before a pause or stop.
	Flush func(ctx context.Context, tenantID string) error
	// OnStateChange is called after every state change (outside locks).
	OnStateChange func(tenantID string, st State)
	// BootTimeout bounds one boot (default 120 s). For a driver with
	// Capabilities.ProviderTimeout, it bounds only the guest-ready wait
	// after the provider part.
	BootTimeout time.Duration
	// CPUs, MemMB, and GPU are reported by Status.
	CPUs  int
	MemMB int
	GPU   string
	// Holds keeps the hold of a user stop across host restarts (optional;
	// without it the hold is in memory only).
	Holds HoldStore
	// Logger and Now are optional.
	Logger *slog.Logger
	Now    func() time.Time
}

// Controller owns the VM lifecycle of every tenant: singleflight boot,
// leases, and idle hibernation.
type Controller struct {
	opts Options
	caps Capabilities

	mu      sync.Mutex
	tenants map[string]*tenantVM
}

type tenantVM struct {
	// trans serializes driver transitions (boot, pause, stop).
	trans sync.Mutex

	state     State
	bootedAt  time.Time
	leases    map[string]int
	idleSince time.Time
	lastError string
	boot      *bootCall
	timer     *time.Timer
	pausing   bool
	stopping  bool
	// held is set by Stop (from its start) and cleared by Start: a VM
	// that the user stopped is not woken on demand (EnsureRunning).
	// holdMu orders the changes of held with their writes to Options.Holds;
	// holdGen counts the changes.
	held    bool
	holdMu  sync.Mutex
	holdGen uint64
}

type bootCall struct {
	done chan struct{}
	err  error
}

// NewController returns a controller. Call Refresh to adopt a VM that
// survived a host restart.
func NewController(opts Options) (*Controller, error) {
	if opts.Driver == nil || opts.Spec == nil {
		return nil, errors.New("vmm: Driver and Spec are required")
	}
	if opts.BootTimeout <= 0 {
		opts.BootTimeout = DefaultBootTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &Controller{opts: opts, caps: CapabilitiesOf(opts.Driver), tenants: map[string]*tenantVM{}}
	if w, ok := opts.Driver.(StateWatcher); ok {
		w.WatchState(c.Observe)
	}
	return c, nil
}

// Driver returns the backend.
func (c *Controller) Driver() Driver { return c.opts.Driver }

func (c *Controller) tenant(tid string) *tenantVM {
	t := c.tenants[tid]
	if t == nil {
		t = &tenantVM{state: StateStopped, leases: map[string]int{}, idleSince: c.opts.Now().UTC()}
		c.tenants[tid] = t
	}
	return t
}

func (c *Controller) setState(tid string, t *tenantVM, st State) {
	c.mu.Lock()
	changed := t.state != st
	t.state = st
	switch st {
	case StateRunning:
		if t.bootedAt.IsZero() {
			t.bootedAt = c.opts.Now().UTC()
		}
		if changed && len(t.leases) == 0 {
			// The idle period starts when the VM becomes usable.
			t.idleSince = c.opts.Now().UTC()
		}
		c.armIdleLocked(tid, t)
	case StateStopped:
		t.bootedAt = time.Time{}
		c.stopTimerLocked(t)
	default:
		c.stopTimerLocked(t)
	}
	c.mu.Unlock()
	if changed {
		c.opts.Logger.Info("vm state", "tenant", tid, "state", st)
		if c.opts.OnStateChange != nil {
			c.opts.OnStateChange(tid, st)
		}
	}
}

// Refresh reads the driver state, adopting a VM that is already running.
func (c *Controller) Refresh(ctx context.Context, tid string) error {
	c.mu.Lock()
	t := c.tenant(tid)
	c.mu.Unlock()
	t.trans.Lock()
	defer t.trans.Unlock()
	st, err := c.opts.Driver.State(ctx, tid)
	if err != nil {
		// The status shows why the VM cannot be read (for example a
		// refused provider key or a deleted machine).
		c.recordError(t, err)
		return err
	}
	if st == StateBooting && !c.caps.Remote {
		// A local VM that was booting when the previous host process
		// stopped is half made; the next boot makes it again. A remote
		// boot continues, and the next boot waits for it (bootLocked).
		st = StateStopped
	}
	c.setState(tid, t, st)
	return nil
}

// EnsureRunning wakes the VM on demand: it boots a stopped VM or resumes a
// paused one and waits until the guest daemon answers. Concurrent calls
// share one boot, which keeps running when a caller's ctx ends. A VM that
// the user stopped (Stop, until Start) is not woken: EnsureRunning returns
// ErrStopped at once.
func (c *Controller) EnsureRunning(ctx context.Context, tid string) error {
	c.mu.Lock()
	t := c.tenant(tid)
	if t.held {
		c.mu.Unlock()
		return ErrStopped
	}
	if t.state == StateRunning && !t.pausing && t.boot == nil {
		c.mu.Unlock()
		return nil
	}
	call := t.boot
	if call == nil {
		call = &bootCall{done: make(chan struct{})}
		t.boot = call
		go c.runBoot(tid, t, call)
	}
	c.mu.Unlock()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AcquireLease records activity that keeps the VM awake (an open desktop
// stream or a running PTY). Acquire the lease before EnsureRunning so an
// idle hibernation cannot slip in between.
func (c *Controller) AcquireLease(tid, kind string) func() {
	c.mu.Lock()
	t := c.tenant(tid)
	t.leases[kind]++
	t.idleSince = time.Time{}
	c.stopTimerLocked(t)
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			t.leases[kind]--
			if t.leases[kind] <= 0 {
				delete(t.leases, kind)
			}
			if len(t.leases) == 0 {
				t.idleSince = c.opts.Now().UTC()
				c.armIdleLocked(tid, t)
			}
		})
	}
}

func (c *Controller) stopTimerLocked(t *tenantVM) {
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
}

func (c *Controller) armIdleLocked(tid string, t *tenantVM) {
	c.stopTimerLocked(t)
	if c.opts.IdleTimeout <= 0 || len(t.leases) > 0 || t.state != StateRunning {
		return
	}
	if t.idleSince.IsZero() {
		t.idleSince = c.opts.Now().UTC()
	}
	wait := c.opts.IdleTimeout - c.opts.Now().Sub(t.idleSince)
	t.timer = time.AfterFunc(max(wait, 0), func() { c.idleExpired(tid) })
}

func (c *Controller) idleExpired(tid string) {
	c.mu.Lock()
	t := c.tenant(tid)
	idle := len(t.leases) == 0 && t.state == StateRunning && t.boot == nil &&
		c.opts.Now().Sub(t.idleSince) >= c.opts.IdleTimeout-time.Second
	if idle {
		t.pausing = true
	}
	c.mu.Unlock()
	if !idle {
		return
	}
	c.opts.Logger.Info("vm idle; hibernating", "tenant", tid, "idleTimeout", c.opts.IdleTimeout)
	err := c.hibernate(context.Background(), tid, true)
	c.mu.Lock()
	t.pausing = false
	c.mu.Unlock()
	if err != nil && !errors.Is(err, ErrConflict) {
		c.opts.Logger.Warn("idle hibernate failed", "tenant", tid, "err", err)
	}
}

// Hibernate flushes guest filesystems and pauses the VM.
func (c *Controller) Hibernate(ctx context.Context, tid string) error {
	return c.hibernate(ctx, tid, false)
}

func (c *Controller) hibernate(ctx context.Context, tid string, onlyIfIdle bool) error {
	c.mu.Lock()
	t := c.tenant(tid)
	c.mu.Unlock()
	t.trans.Lock()
	defer t.trans.Unlock()
	c.mu.Lock()
	st, leases := t.state, len(t.leases)
	c.mu.Unlock()
	if st != StateRunning {
		return fmt.Errorf("%w: hibernate while %s", ErrConflict, st)
	}
	if onlyIfIdle && leases > 0 {
		return fmt.Errorf("%w: leases appeared", ErrConflict)
	}
	// While the pause runs, the VM is not used: a wake waits for the pause
	// and then resumes the VM, and Paused reports true.
	c.mu.Lock()
	t.pausing = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		t.pausing = false
		c.mu.Unlock()
	}()
	c.flush(ctx, tid)
	if err := c.opts.Driver.Pause(ctx, tid); err != nil {
		c.recordError(t, err)
		return fmt.Errorf("vmm: pause: %w", err)
	}
	c.setState(tid, t, StatePaused)
	c.clearError(t)
	return nil
}

func (c *Controller) flush(ctx context.Context, tid string) error {
	if c.opts.Flush == nil {
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, FlushTimeout)
	defer cancel()
	err := c.opts.Flush(fctx, tid)
	if err != nil {
		c.opts.Logger.Warn("guest filesystem sync failed", "tenant", tid, "err", err)
	}
	return err
}

// clearError forgets the error of an earlier failed transition after one
// that worked, so the status does not show an old failure.
func (c *Controller) clearError(t *tenantVM) {
	c.mu.Lock()
	t.lastError = ""
	c.mu.Unlock()
}

func (c *Controller) recordError(t *tenantVM, err error) {
	c.mu.Lock()
	t.lastError = err.Error()
	c.mu.Unlock()
}

func (c *Controller) stop(ctx context.Context, tid string, t *tenantVM, force bool) error {
	t.trans.Lock()
	defer t.trans.Unlock()
	c.mu.Lock()
	st := t.state
	c.mu.Unlock()
	if st == StateStopped {
		if ds, err := c.opts.Driver.State(ctx, tid); err != nil || ds == StateStopped {
			return fmt.Errorf("%w: stop while %s", ErrConflict, st)
		}
	}
	// Other clients see the stop while it runs (Status.Transition).
	c.mu.Lock()
	t.stopping = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		t.stopping = false
		c.mu.Unlock()
	}()
	if st == StatePaused && !force {
		if err := c.opts.Driver.Resume(ctx, tid); err != nil {
			return fmt.Errorf("vmm: resume before stop: %w", err)
		}
	}
	if !force || st == StateRunning {
		if err := c.flush(ctx, tid); err != nil && !force {
			return fmt.Errorf("%w: sync guest filesystems before stop: %v (retry with force)", ErrUnavailable, err)
		}
	}
	if err := c.opts.Driver.Shutdown(ctx, tid, force); err != nil {
		c.recordError(t, err)
		return fmt.Errorf("vmm: shutdown: %w", err)
	}
	c.setState(tid, t, StateStopped)
	c.clearError(t)
	return nil
}

// Close stops every running VM (host shutdown, before storage unmounts).
// A remote VM (Capabilities.Remote) is left as it is: it keeps its files,
// the next host process adopts it, and the provider's idle pause applies.
// A host shutdown is not a user stop: it does not set the hold.
func (c *Controller) Close(ctx context.Context) error {
	c.mu.Lock()
	running := map[string]*tenantVM{}
	for id, t := range c.tenants {
		c.stopTimerLocked(t)
		if t.state != StateStopped && !c.caps.Remote {
			running[id] = t
		}
	}
	c.mu.Unlock()
	var errs []error
	for id, t := range running {
		if err := c.stop(ctx, id, t, true); err != nil && !errors.Is(err, ErrConflict) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// State returns the cached state.
func (c *Controller) State(tid string) State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tenant(tid).state
}

// Status returns the VMStatus object of docs/api.md section 7.
func (c *Controller) Status(tid string) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tenant(tid)
	leases := map[string]int{LeasePTY: 0, LeaseDesktop: 0}
	for k, v := range t.leases {
		leases[k] = v
	}
	st := Status{
		State: string(t.state), Driver: c.opts.Driver.Name(), CPUs: c.opts.CPUs, MemMB: c.opts.MemMB, GPU: c.opts.GPU,
		BootedAt: t.bootedAt, Leases: leases, IdleTimeoutSeconds: int64(c.opts.IdleTimeout / time.Second),
		IdleSince: t.idleSince, LastError: t.lastError,
	}
	switch {
	case t.stopping:
		st.Transition = TransitionStopping
	case t.pausing && t.state == StateRunning:
		st.Transition = TransitionPausing
	}
	if b, ok := c.opts.Driver.(StartBlocker); ok && t.state != StateRunning {
		if until := b.StartBlockedUntil(tid); until.After(c.opts.Now()) {
			u := until.UTC()
			st.StartBlockedUntil = &u
		}
	}
	return st
}

// VMStatus implements api.VMStatusSource.
func (c *Controller) VMStatus(_ context.Context, tid string) (api.VMStatus, error) {
	st := c.Status(tid)
	return api.VMStatus{State: st.State, Driver: st.Driver}, nil
}
