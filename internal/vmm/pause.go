package vmm

import (
	"context"
	"errors"
	"fmt"
)

// Pause of a VM (Hibernate and the idle hibernation). A pause that started
// runs to its end: the context of the caller does not cancel it, because a
// pause cut in the middle leaves a VM that runs without its guest link.
// The driver call has the bound of the provider part of a boot
// (Capabilities.ProviderTimeout for a remote VM, else Options.BootTimeout);
// the filesystem sync before it has FlushTimeout. While the pause runs,
// Status shows the transition "pausing", Serving is false, and a wake
// waits for the pause and then resumes the VM.
//
// After a failed pause, the controller reads the driver state, so that the
// cached state stays true: PAUSED when the provider paused the machine (or
// still pauses it), else the state that the driver reports. A remote VM
// that still runs gets its guest link again (relink).

// Hibernate flushes guest filesystems and pauses the VM. When ctx ends
// before the pause, Hibernate returns ErrPausing and the pause continues.
func (c *Controller) Hibernate(ctx context.Context, tid string) error {
	done := make(chan error, 1)
	go func() { done <- c.hibernate(context.WithoutCancel(ctx), tid, false) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		select {
		case err := <-done:
			return err
		default:
		}
		return ErrPausing
	}
}

// hibernate runs one pause. ctx must not be cancelled by a caller that
// goes away (Hibernate gives it context.WithoutCancel).
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
	pctx, cancel := context.WithTimeout(ctx, c.providerBound())
	err := c.opts.Driver.Pause(pctx, tid)
	timedOut := err != nil && errors.Is(pctx.Err(), context.DeadlineExceeded)
	cancel()
	if err != nil {
		if timedOut {
			err = &ProviderTimeoutError{Action: "pause", Bound: c.providerBound(), Local: c.caps.ProviderTimeout <= 0}
		} else {
			err = fmt.Errorf("vmm: pause: %w", err)
		}
		return c.pauseFailed(tid, t, err)
	}
	c.mu.Lock()
	t.relink = false
	c.mu.Unlock()
	c.setState(tid, t, StatePaused)
	c.clearError(t)
	return nil
}

// pauseFailed sets the state after a failed pause from the driver's view
// and returns the error of the pause (nil when the machine is paused or
// pauses at the provider). Callers hold t.trans.
func (c *Controller) pauseFailed(tid string, t *tenantVM, perr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	st, err := c.opts.Driver.State(ctx, tid)
	cancel()
	switch {
	case err == nil && st == StatePaused:
		// The provider paused the machine, or still pauses it: a wake
		// waits for the pause (the driver's resume) and then resumes it.
		c.opts.Logger.Warn("vm pause reported an error, but the machine is paused", "tenant", tid, "err", perr)
		c.mu.Lock()
		t.relink = false
		c.mu.Unlock()
		c.setState(tid, t, StatePaused)
		c.clearError(t)
		return nil
	case !c.caps.Remote:
		// A local VM keeps its guest connection during a pause attempt.
		if err == nil && st != StateRunning {
			c.setState(tid, t, st)
		}
	case err == nil && st == StateStopped:
		c.setState(tid, t, StateStopped)
	default:
		// The machine runs (or its state is not known): the pause closed
		// the guest link. Attach it again now; when that fails too, the
		// next wake attaches it (bootLocked).
		c.mu.Lock()
		t.relink = true
		c.mu.Unlock()
		if err == nil && st == StateRunning {
			if rerr := c.relinkNow(tid); rerr != nil {
				c.opts.Logger.Warn("attach the guest link after a failed pause; the next use tries again", "tenant", tid, "err", rerr)
			} else {
				c.mu.Lock()
				t.relink = false
				c.mu.Unlock()
				c.opts.Logger.Info("guest link attached again after a failed pause", "tenant", tid)
			}
		}
	}
	c.recordError(t, perr)
	return perr
}

// relinkNow attaches the guest link of a remote machine that runs (the
// driver's Resume of a running machine attaches it) and waits until the
// guest answers.
func (c *Controller) relinkNow(tid string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.providerBound())
	defer cancel()
	if err := c.opts.Driver.Resume(ctx, tid); err != nil {
		return fmt.Errorf("vmm: attach the guest link: %w", err)
	}
	rctx, rcancel := context.WithTimeout(context.Background(), c.opts.BootTimeout)
	defer rcancel()
	return c.waitReady(rctx, tid)
}
