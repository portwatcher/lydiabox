package instabox

import (
	"context"
	"errors"
	"time"

	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// WatchState implements vmm.StateWatcher. The watcher reads the state of
// each known machine every PollInterval, and at once when the guest link
// drops or a lease renewal fails. It reports a change that this driver did
// not make: an idle pause by InstaBox, a resume by another client, a stop,
// or a deleted machine (reported as stopped).
func (d *Driver) WatchState(fn func(tenantID string, st vmm.State)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.watch != nil || d.closed {
		return
	}
	d.watch = fn
	ctx, cancel := context.WithCancel(context.Background())
	d.stop, d.done = cancel, make(chan struct{})
	go d.watchLoop(ctx, d.done)
}

func (d *Driver) watchLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	timer := time.NewTimer(d.opts.PollInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-d.kick:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		d.pollAll(ctx)
		timer.Reset(d.opts.PollInterval)
	}
}

// pollAll reads the state of every tenant with a known machine.
func (d *Driver) pollAll(ctx context.Context) {
	d.mu.Lock()
	ids := map[string]string{}
	for tid, t := range d.tenants {
		if t.machineID != "" {
			ids[tid] = t.machineID
		}
	}
	fn := d.watch
	d.mu.Unlock()
	for tid, id := range ids {
		if ctx.Err() != nil {
			return
		}
		st, ok := d.poll(ctx, tid, id)
		if ok && fn != nil {
			fn(tid, st)
		}
	}
}

// poll reads one machine. ok is true when its power state changed since
// the last report.
func (d *Driver) poll(ctx context.Context, tid, id string) (vmm.State, bool) {
	if d.gate.check(tid, d.opts.Now()) != nil {
		return "", false
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	m, err := d.opts.Client.GetMachine(rctx, id)
	cancel()
	var st vmm.State
	switch {
	case errors.Is(err, ib.ErrNotFound):
		st = vmm.StateStopped
		d.opts.Logger.Error("instabox: the machine of the tenant no longer exists", "tenant", tid, "machine", id)
	case err != nil:
		if ctx.Err() == nil {
			d.opts.Logger.Warn("instabox: read the machine state", "tenant", tid, "err", err)
		}
		return "", false
	default:
		st = mapState(m)
	}
	t := d.tenant(tid)
	d.mu.Lock()
	changed := st != t.reported && st != vmm.StateBooting
	if changed {
		t.reported = st
	}
	selfPaused := changed && st != vmm.StateRunning && !t.down
	if st == vmm.StateRunning && t.down && changed {
		// Another client resumed the machine: attach again on the next dial.
		t.down, t.attached = false, false
	}
	d.mu.Unlock()
	if selfPaused {
		// InstaBox paused or stopped the machine by itself. Close the link,
		// because its reconnects would wake the machine again, and end the
		// lease; the next use resumes the machine through the controller.
		d.opts.Logger.Info("instabox: the machine changed without this cell", "tenant", tid, "state", st)
		d.detach(tid)
	}
	return st, changed
}
