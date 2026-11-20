package vmm

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Boot of a VM (EnsureRunning). A local boot has one bound, BootTimeout,
// for the driver calls and the guest-ready wait. A remote driver with
// Capabilities.ProviderTimeout has two: ProviderTimeout for the provider
// part (create and boot, or resume), then BootTimeout for the guest-ready
// wait. During the full boot the state stays BOOTING.

// cleanupTimeout bounds the driver calls after a failed boot.
const cleanupTimeout = 30 * time.Second

// ProviderTimeoutError is the error of a remote boot that reached
// Capabilities.ProviderTimeout. The provider can still bring the machine
// up; the controller does not stop it, and the state watcher adopts it
// when it runs. It wraps ErrUnavailable.
type ProviderTimeoutError struct {
	// Action is "start" or "resume".
	Action string
	// Bound is the provider bound that ended.
	Bound time.Duration
}

func (e *ProviderTimeoutError) Error() string {
	return fmt.Sprintf("the VM provider did not %s the machine in %s; the machine can still start, and LydiaBox uses it when it runs",
		e.Action, humanDuration(e.Bound))
}

// Unwrap returns ErrUnavailable (503 in the API).
func (e *ProviderTimeoutError) Unwrap() error { return ErrUnavailable }

// humanDuration writes d for a person: "10 minutes" for whole minutes.
func humanDuration(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "1 minute"
	case d > time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	case d >= time.Second:
		return d.Round(time.Second).String()
	}
	return d.String()
}

func (c *Controller) runBoot(tid string, t *tenantVM, call *bootCall) {
	t.trans.Lock()
	err := c.bootLocked(tid, t)
	t.trans.Unlock()
	c.mu.Lock()
	t.boot = nil
	if err != nil {
		t.lastError = err.Error()
	} else {
		t.lastError = ""
	}
	c.mu.Unlock()
	call.err = err
	close(call.done)
}

// providerBound is the bound of the driver calls of one boot.
func (c *Controller) providerBound() time.Duration {
	if c.caps.ProviderTimeout > 0 {
		return c.caps.ProviderTimeout
	}
	return c.opts.BootTimeout
}

// providerTimeout returns a *ProviderTimeoutError when ctx, the provider
// context of a remote boot, ended by its deadline; else nil.
func (c *Controller) providerTimeout(ctx context.Context, action string) error {
	if c.caps.ProviderTimeout <= 0 || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return &ProviderTimeoutError{Action: action, Bound: c.caps.ProviderTimeout}
}

func (c *Controller) bootLocked(tid string, t *tenantVM) error {
	c.mu.Lock()
	held := t.held
	c.mu.Unlock()
	if held {
		// A Stop came after this wake was asked for.
		return ErrStopped
	}
	pctx, cancel := context.WithTimeout(context.Background(), c.providerBound())
	defer cancel()
	st, err := c.opts.Driver.State(pctx, tid)
	if err != nil {
		return fmt.Errorf("vmm: read VM state: %w", err)
	}
	// before is the state that a failed remote boot goes back to: the
	// next wake then calls the driver again.
	before, adopt := StateStopped, false
	switch st {
	case StateRunning:
		// Already running (adopted after a host restart or a cancelled pause).
	case StatePaused:
		before = StatePaused
		c.setState(tid, t, StateBooting)
		if err := c.opts.Driver.Resume(pctx, tid); err != nil {
			c.setState(tid, t, StatePaused)
			if pt := c.providerTimeout(pctx, "resume"); pt != nil {
				return pt
			}
			return fmt.Errorf("vmm: resume: %w", err)
		}
	case StateBooting:
		if c.caps.Remote {
			// The provider is still booting the machine: wait for the
			// guest, do not create it again.
			c.setState(tid, t, StateBooting)
			adopt = true
			break
		}
		fallthrough
	default:
		c.setState(tid, t, StateBooting)
		c.mu.Lock()
		t.bootedAt = time.Time{}
		c.mu.Unlock()
		if err := c.create(pctx, tid); err != nil {
			if pt := c.providerTimeout(pctx, "start"); pt != nil {
				c.setState(tid, t, before)
				return pt
			}
			c.cleanupFailedBoot(tid, t, before)
			return err
		}
	}
	// The guest-ready wait: within the provider bound for an adopted boot
	// (the provider still boots the machine), else with its own bound
	// after the provider part of a remote boot.
	rctx := pctx
	if c.caps.ProviderTimeout > 0 && !adopt {
		var rcancel context.CancelFunc
		rctx, rcancel = context.WithTimeout(context.Background(), c.opts.BootTimeout)
		defer rcancel()
	}
	if err := c.waitReady(rctx, tid); err != nil {
		c.cleanupFailedBoot(tid, t, before)
		if adopt {
			if pt := c.providerTimeout(pctx, "start"); pt != nil {
				return pt
			}
		}
		return err
	}
	c.mu.Lock()
	t.pausing = false
	c.mu.Unlock()
	c.setState(tid, t, StateRunning)
	return nil
}

func (c *Controller) create(ctx context.Context, tid string) error {
	spec, err := c.opts.Spec(ctx, tid)
	if err != nil {
		return fmt.Errorf("vmm: build VM spec: %w", err)
	}
	if err := c.opts.Driver.Create(ctx, tid, spec); err != nil {
		return fmt.Errorf("vmm: create VM: %w", err)
	}
	if err := c.opts.Driver.Boot(ctx, tid); err != nil {
		return fmt.Errorf("vmm: boot VM: %w", err)
	}
	return nil
}

// cleanupFailedBoot handles a VM after a failed boot. A local VM is torn
// down, so that the next attempt starts clean. A remote VM is never
// stopped: a forced stop of a machine that the provider just resumed ends
// the user's processes. See remoteFailedBoot.
func (c *Controller) cleanupFailedBoot(tid string, t *tenantVM, before State) {
	if c.caps.Remote {
		c.remoteFailedBoot(tid, t, before)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := c.opts.Driver.Shutdown(ctx, tid, true); err != nil {
		c.opts.Logger.Warn("cleanup after failed boot", "tenant", tid, "err", err)
	}
	c.setState(tid, t, StateStopped)
}

// remoteFailedBoot sets the state of a remote VM after a failed boot from
// the provider's view. A machine that runs although its guest did not
// answer is paused: the pause keeps its memory and processes, and ends
// the activity that keeps the machine awake. A machine that the provider
// still boots, or a state that cannot be read, gives before (the state
// before the boot); the state watcher reports the machine when it runs.
func (c *Controller) remoteFailedBoot(tid string, t *tenantVM, before State) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	st, err := c.opts.Driver.State(ctx, tid)
	switch {
	case err != nil || st == StateBooting:
		st = before
	case st == StateRunning:
		if err := c.opts.Driver.Pause(ctx, tid); err != nil {
			c.opts.Logger.Warn("pause after failed boot", "tenant", tid, "err", err)
			st = before
		} else {
			st = StatePaused
		}
	}
	c.setState(tid, t, st)
}

func (c *Controller) waitReady(ctx context.Context, tid string) error {
	if c.opts.Ready == nil {
		return nil
	}
	probe := c.caps.ReadyProbeTimeout
	if probe <= 0 {
		probe = DefaultReadyProbeTimeout
	}
	var last error
	for {
		rctx, cancel := context.WithTimeout(ctx, probe)
		last = c.opts.Ready(rctx, tid)
		cancel()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("vmm: guest daemon not ready: %w (%v)", ctx.Err(), last)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
