package instabox

import (
	"context"
	"fmt"
	"strings"
	"time"

	ib "github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/vmm"
	"github.com/portwatcher/lydiabox/internal/vmm/link"
)

// DefaultGuestStatusAfter is how long the port proxy answers 502 before
// the driver logs the status of the image's programs.
const DefaultGuestStatusAfter = 45 * time.Second

// guestStatusCmd prints the state of the image's unit and of its
// supervisord programs, and the last lines of the unit's journal (the
// entry point's errors and the program state changes). It prints no
// secret: the guest daemon logs to a file, and guest.env is not read.
const guestStatusCmd = "systemctl is-active instabox-image.service; " +
	"sudo -n supervisorctl -c /etc/lydiabox/instabox-supervisord.conf status; " +
	"sudo -n journalctl -u instabox-image.service -n 5 -o cat --no-pager"

// maxStatusLen bounds the status in the log line.
const maxStatusLen = 1500

// badGateway reports whether a link error is the port proxy's 502: the
// machine runs, but nothing listens on the link port.
func badGateway(s string) bool { return strings.Contains(s, "HTTP 502") }

// dialError replaces a link error of the port proxy's 502 with a clear
// one for the API and the web status bar.
func (d *Driver) dialError(err error) error {
	if err == nil || !badGateway(err.Error()) {
		return err
	}
	return fmt.Errorf("%w: the InstaBox machine runs, but its guest daemon does not answer on port %d "+
		"(the port proxy answers 502 Bad Gateway); see 'journalctl -u instabox-image.service' in the machine",
		vmm.ErrUnavailable, d.opts.LinkPort)
}

// noteLink starts the timer of the status line when the link fails with
// the port proxy's 502, once for each connection of the link.
func (d *Driver) noteLink(tid string, s link.Status) {
	t := d.tenant(tid)
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.State == link.StateUp {
		t.statusLogged = false
		return
	}
	if s.State != link.StateDown || !badGateway(s.LastError) || d.closed || t.statusLogged || t.statusTimer != nil {
		return
	}
	id := t.machineID
	t.statusTimer = time.AfterFunc(d.opts.GuestStatusAfter, func() { d.logGuestStatus(tid, id) })
}

// logGuestStatus logs one line with the status of the image's programs
// when the link still gets 502 from the port proxy.
func (d *Driver) logGuestStatus(tid, id string) {
	t := d.tenant(tid)
	d.mu.Lock()
	t.statusTimer = nil
	skip := d.closed || t.down || id == "" || t.machineID != id
	if !skip {
		t.statusLogged = true
	}
	d.mu.Unlock()
	if skip {
		return
	}
	l, err := d.links.For(tid)
	if err != nil {
		return
	}
	if s := l.Status(); s.State != link.StateDown || !badGateway(s.LastError) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := d.opts.Client.Exec(ctx, id, ib.ExecRequest{Cmd: guestStatusCmd, TimeoutSeconds: 20, NoHistory: true})
	if err != nil {
		d.opts.Logger.Warn("instabox: the guest daemon does not answer; the status command failed",
			"tenant", tid, "machine", id, "err", err)
		return
	}
	d.opts.Logger.Warn("instabox: the guest daemon does not answer; machine status",
		"tenant", tid, "machine", id, "status", oneLine(res.Stdout+"\n"+res.Stderr, maxStatusLen))
}

// stopStatusTimersLocked stops the pending status timers (Close). Callers hold
// d.mu.
func (d *Driver) stopStatusTimersLocked() {
	for _, t := range d.tenants {
		if t.statusTimer != nil {
			t.statusTimer.Stop()
			t.statusTimer = nil
		}
	}
}

// oneLine joins the non-empty lines of s with " | " and cuts the result
// at limit bytes.
func oneLine(s string, limit int) string {
	var parts []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	out := strings.Join(parts, " | ")
	if len(out) > limit {
		out = out[:limit] + "..."
	}
	return out
}
