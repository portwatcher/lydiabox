package vmm

import (
	"context"
	"fmt"
)

// RootResetter is implemented by drivers that can discard the persistent
// root of a stopped VM (the docker driver removes its container).
type RootResetter interface {
	ResetRoot(ctx context.Context, tenantID string) error
}

// ResetRoot discards the persistent root file system changes of a stopped
// VM, so the next boot starts from the image. /home/agent and /workspace
// are on the JuiceFS volume and are not changed.
func (c *Controller) ResetRoot(ctx context.Context, tid string) error {
	c.mu.Lock()
	t := c.tenant(tid)
	c.mu.Unlock()
	t.trans.Lock()
	defer t.trans.Unlock()
	c.mu.Lock()
	st := t.state
	c.mu.Unlock()
	if st != StateStopped {
		return fmt.Errorf("%w: reset the root while %s (stop the VM first)", ErrConflict, st)
	}
	rr, ok := c.opts.Driver.(RootResetter)
	if !ok {
		return fmt.Errorf("%w: the %s driver keeps the root in root-upper.img; delete that file to reset it", ErrConflict, c.opts.Driver.Name())
	}
	if err := rr.ResetRoot(ctx, tid); err != nil {
		c.recordError(t, err)
		return fmt.Errorf("vmm: reset root: %w", err)
	}
	return nil
}
