package vmm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestStatusShowsTransitions: a pause or stop that runs is in the status,
// so a client that did not start it does not show RUNNING with its power
// controls.
func TestStatusShowsTransitions(t *testing.T) {
	for _, tc := range []struct {
		want string
		run  func(c *Controller) error
	}{
		{TransitionPausing, func(c *Controller) error { return c.Hibernate(context.Background(), "default") }},
		{TransitionStopping, func(c *Controller) error { return c.Stop(context.Background(), "default", false) }},
	} {
		t.Run(tc.want, func(t *testing.T) {
			var (
				release = make(chan struct{})
				entered = make(chan struct{})
				arm     sync.Once
				armed   bool
				mu      sync.Mutex
			)
			c := newCtl(t, &fakeDriver{}, 0, func(context.Context, string) error {
				mu.Lock()
				on := armed
				mu.Unlock()
				if on {
					arm.Do(func() { close(entered); <-release })
				}
				return nil
			})
			if err := c.EnsureRunning(context.Background(), "default"); err != nil {
				t.Fatal(err)
			}
			if st := c.Status("default"); st.Transition != "" {
				t.Fatalf("running: transition %q", st.Transition)
			}
			mu.Lock()
			armed = true
			mu.Unlock()
			done := make(chan error, 1)
			go func() { done <- tc.run(c) }()
			<-entered
			if st := c.Status("default"); st.Transition != tc.want || st.State != string(StateRunning) {
				t.Fatalf("during: state %s transition %q", st.State, st.Transition)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if st := c.Status("default"); st.Transition != "" {
				t.Fatalf("after: transition %q", st.Transition)
			}
		})
	}
}

type blockedDriver struct {
	*fakeDriver
	until time.Time
}

func (b *blockedDriver) StartBlockedUntil(string) time.Time { return b.until }

// TestStatusShowsAStartBlock: while the provider refuses a start, the status
// says until when; a running VM and a past time show none.
func TestStatusShowsAStartBlock(t *testing.T) {
	d := &blockedDriver{fakeDriver: &fakeDriver{}, until: time.Now().Add(10 * time.Minute)}
	c, err := NewController(Options{Driver: d, CPUs: 4, MemMB: 8192, Logger: newCtl(t, &fakeDriver{}, 0, nil).opts.Logger,
		Spec:  func(context.Context, string) (Spec, error) { return Spec{CPUs: 4}, nil },
		Ready: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	st := c.Status("default")
	if st.StartBlockedUntil == nil || !st.StartBlockedUntil.Equal(d.until.UTC()) {
		t.Fatalf("blocked = %v, want %v", st.StartBlockedUntil, d.until)
	}
	d.until = time.Now().Add(-time.Second)
	if st := c.Status("default"); st.StartBlockedUntil != nil {
		t.Fatalf("past block = %v", st.StartBlockedUntil)
	}
}

// TestAWorkingTransitionClearsAnOldError: after a failed pause, a stop that
// works does not leave the pause error in the status.
func TestAWorkingTransitionClearsAnOldError(t *testing.T) {
	c := newCtl(t, &fakeDriver{}, 0, nil)
	ctx := context.Background()
	if err := c.EnsureRunning(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	c.recordError(c.tenant("default"), errors.New("pause failed"))
	if err := c.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	if st := c.Status("default"); st.LastError != "" {
		t.Fatalf("after a stop: lastError %q", st.LastError)
	}
}
