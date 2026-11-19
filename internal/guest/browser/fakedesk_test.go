package browser

import (
	"context"
	"sync"

	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

// fakeDesktop records events and can refuse actions like a takeover.
type fakeDesktop struct {
	mu       sync.Mutex
	events   []x11.AgentAction
	takeover bool
	acts     int
}

func (d *fakeDesktop) Act(_ context.Context, ev x11.AgentAction, fn func() error) error {
	d.mu.Lock()
	d.acts++
	busy := d.takeover
	d.mu.Unlock()
	if busy {
		return x11.ErrTakeover
	}
	if err := fn(); err != nil {
		return err
	}
	if ev.Action != "" {
		d.Observe(ev)
	}
	return nil
}

func (d *fakeDesktop) Observe(ev x11.AgentAction) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, ev)
}

func (d *fakeDesktop) actions() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, e := range d.events {
		out = append(out, e.Action+":"+e.Tool)
	}
	return out
}
