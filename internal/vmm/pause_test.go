package vmm

import (
	"context"
	"testing"
)

// During an explicit Hibernate the VM reports Paused and not Serving,
// before the driver pause returns; a Stop in progress is not Serving.
func TestPausedAndServingFollowTheTransitions(t *testing.T) {
	type seen struct{ paused, serving bool }
	var during []seen
	var c *Controller
	c = newCtl(t, &fakeDriver{}, 0, func(context.Context, string) error {
		during = append(during, seen{c.Paused("default"), c.Serving("default")})
		return nil
	})
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if c.Paused("default") || !c.Serving("default") {
		t.Fatalf("running: paused %v serving %v", c.Paused("default"), c.Serving("default"))
	}
	if err := c.Hibernate(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if !c.Paused("default") || c.Serving("default") {
		t.Fatalf("paused: paused %v serving %v", c.Paused("default"), c.Serving("default"))
	}
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if c.Paused("default") || !c.Serving("default") {
		t.Fatalf("resumed: paused %v serving %v", c.Paused("default"), c.Serving("default"))
	}
	if err := c.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	want := []seen{{paused: true}, {paused: false, serving: false}}
	if len(during) != 2 || during[0] != want[0] || during[1] != want[1] {
		t.Fatalf("during hibernate and stop: %+v, want %+v", during, want)
	}
}

// A wake asked for while a Hibernate runs waits for the pause and then
// resumes the VM.
func TestWakeDuringHibernateResumesAfterThePause(t *testing.T) {
	d := &fakeDriver{}
	var c *Controller
	woke := make(chan error, 1)
	c = newCtl(t, d, 0, func(context.Context, string) error {
		if d.count("pause") == 0 && len(woke) == 0 && c.Paused("default") {
			go func() { woke <- c.EnsureRunning(context.Background(), "default") }()
		}
		return nil
	})
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := c.Hibernate(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := <-woke; err != nil {
		t.Fatal(err)
	}
	if c.State("default") != StateRunning || d.Calls() != "create,boot,pause,resume" {
		t.Fatalf("state %s calls %s", c.State("default"), d.Calls())
	}
}
