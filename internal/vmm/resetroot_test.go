package vmm

import (
	"context"
	"errors"
	"testing"
)

type resetDriver struct {
	fakeDriver
	resets int
}

func (d *resetDriver) ResetRoot(context.Context, string) error {
	d.resets++
	return nil
}

func TestResetRoot(t *testing.T) {
	ctx := context.Background()
	plain := newCtl(t, &fakeDriver{}, 0, nil)
	if err := plain.ResetRoot(ctx, "default"); !errors.Is(err, ErrConflict) {
		t.Fatalf("driver without reset: %v", err)
	}

	d := &resetDriver{}
	c := newCtl(t, &d.fakeDriver, 0, nil)
	c.opts.Driver = d
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.ResetRoot(ctx, "default"); !errors.Is(err, ErrConflict) || d.resets != 0 {
		t.Fatalf("reset while running: %v", err)
	}
	if err := c.Stop(ctx, "default", true); err != nil {
		t.Fatal(err)
	}
	if err := c.ResetRoot(ctx, "default"); err != nil || d.resets != 1 {
		t.Fatalf("reset while stopped: %v, resets %d", err, d.resets)
	}
}
