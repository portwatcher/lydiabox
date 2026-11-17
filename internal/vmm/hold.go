package vmm

import (
	"context"
	"time"
)

// holdWriteTimeout bounds one write of the hold to Options.Holds. The
// write does not use the request context: a client that goes away must
// not leave the stored hold different from the hold in memory.
const holdWriteTimeout = 10 * time.Second

// HoldStore keeps the hold of a user stop for each tenant across host
// restarts (implemented by store.Store, table vm_holds).
type HoldStore interface {
	VMHold(ctx context.Context, tenantID string) (bool, time.Time, error)
	SetVMHold(ctx context.Context, tenantID string, held bool) error
}

// Stop flushes guest filesystems and shuts the VM down. Without force a
// failed flush aborts the stop; with force the VM is shut down anyway.
// From its start until Start, the VM is held stopped: EnsureRunning does
// not wake it. The hold is written to Options.Holds before the shutdown;
// a failed or refused stop restores the previous hold, in memory and in
// Options.Holds.
func (c *Controller) Stop(ctx context.Context, tid string, force bool) error {
	c.mu.Lock()
	t := c.tenant(tid)
	c.mu.Unlock()
	wasHeld, gen := c.setHold(tid, t, true)
	err := c.stop(ctx, tid, t, force)
	if err != nil {
		c.restoreHold(tid, t, wasHeld, gen)
	}
	return err
}

// Start is an explicit start by the user: it lifts the hold of Stop (in
// memory and in Options.Holds) and then boots or resumes the VM like
// EnsureRunning. A failed write of the hold does not stop the start.
func (c *Controller) Start(ctx context.Context, tid string) error {
	c.mu.Lock()
	t := c.tenant(tid)
	c.mu.Unlock()
	c.setHold(tid, t, false)
	return c.EnsureRunning(ctx, tid)
}

// Held reports whether the user stopped the VM (Stop, until Start).
func (c *Controller) Held(tid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tenant(tid).held
}

// LoadHold reads the hold of a user stop from Options.Holds. Call it after
// Refresh and before the host serves requests. A stored hold of a VM that
// runs is dropped: the stop did not finish (for example the host process
// ended during the shutdown), and a running VM must stay usable. On a read
// error the VM is not held, so an explicit Start and a wake on demand work.
func (c *Controller) LoadHold(ctx context.Context, tid string) error {
	if c.opts.Holds == nil {
		return nil
	}
	held, since, err := c.opts.Holds.VMHold(ctx, tid)
	if err != nil {
		c.opts.Logger.Warn("read the VM stop hold; the VM is not held", "tenant", tid, "err", err)
		return err
	}
	if !held {
		return nil
	}
	c.mu.Lock()
	t := c.tenant(tid)
	st := t.state
	c.mu.Unlock()
	if st != StateStopped && st != StatePaused {
		c.opts.Logger.Warn("the VM runs although the user stopped it; the stop hold is dropped",
			"tenant", tid, "state", st, "heldSince", since)
		c.setHold(tid, t, false)
		return nil
	}
	c.mu.Lock()
	t.held = true
	t.holdGen++
	c.mu.Unlock()
	c.opts.Logger.Info("the VM stays stopped until a start", "tenant", tid, "heldSince", since)
	return nil
}

// setHold changes the hold and writes it to Options.Holds. It returns the
// previous hold and the generation of this change.
func (c *Controller) setHold(tid string, t *tenantVM, held bool) (bool, uint64) {
	t.holdMu.Lock()
	defer t.holdMu.Unlock()
	c.mu.Lock()
	was := t.held
	t.held = held
	t.holdGen++
	gen := t.holdGen
	c.mu.Unlock()
	c.writeHold(tid, held)
	return was, gen
}

// restoreHold puts back the hold that a failed Stop replaced, unless the
// hold changed again after that Stop (for example a Start).
func (c *Controller) restoreHold(tid string, t *tenantVM, was bool, gen uint64) {
	t.holdMu.Lock()
	defer t.holdMu.Unlock()
	c.mu.Lock()
	if t.holdGen != gen {
		c.mu.Unlock()
		return
	}
	t.held = was
	t.holdGen++
	c.mu.Unlock()
	if !was {
		c.writeHold(tid, false)
	}
}

func (c *Controller) writeHold(tid string, held bool) {
	if c.opts.Holds == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), holdWriteTimeout)
	defer cancel()
	if err := c.opts.Holds.SetVMHold(ctx, tid, held); err != nil {
		c.opts.Logger.Warn("write the VM stop hold", "tenant", tid, "held", held, "err", err)
	}
}
