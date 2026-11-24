package instabox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	guestlink "github.com/portwatcher/lydiabox/internal/guest/link"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	ib "github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/vmm"
	"github.com/portwatcher/lydiabox/internal/vmm/link"
)

// guestEnv is the content of /data/.lydiabox/guest.env
// (deploy/instabox/guest.env.example). It holds the link secret, never an
// LLM key, a refresh token, or the InstaBox API key. The file roots are
// not in it: the image owns its disk layout (the agent home
// /data/lydiabox/home and the workspace /data/workspace), and its wrapper
// ignores root settings in this file.
func guestEnv(secret string, port int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s=ws://0.0.0.0:%d/\n", rpc.EnvGuestListen, port)
	fmt.Fprintf(&b, "%s=%s\n", guestlink.EnvSecret, secret)
	fmt.Fprintf(&b, "%s=0\n", rpc.EnvWebRTCPort)
	return []byte(b.String())
}

// LinkURL turns the HTTPS URL of a published port into the WebSocket URL
// of the guest link (the listener path is "/").
func LinkURL(portURL string) (string, error) {
	u, err := url.Parse(portURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("instabox: invalid port URL %q", portURL)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("instabox: invalid port URL %q", portURL)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// attachRunning attaches to a machine that runs already (the first dial
// after a start of this process). It does not start the machine.
func (d *Driver) attachRunning(ctx context.Context, tid string) error {
	if err := d.gate.check(tid, d.opts.Now()); err != nil {
		return err
	}
	t := d.tenant(tid)
	t.op.Lock()
	defer t.op.Unlock()
	d.mu.Lock()
	done := t.attached || t.down
	d.mu.Unlock()
	if done {
		return nil
	}
	id, err := d.machineID(ctx, tid)
	if err != nil {
		return err
	}
	m, err := d.opts.Client.GetMachine(ctx, id)
	if err != nil {
		return d.fail(tid, err)
	}
	if m.State != ib.StateRunning {
		return fmt.Errorf("the machine is %s", m.State)
	}
	return d.attach(ctx, tid, m)
}

// attach prepares a running machine for the guest link: the private port,
// the guest settings, the activity lease, and the link. Callers hold t.op.
func (d *Driver) attach(ctx context.Context, tid string, m *ib.Machine) error {
	rec, sec, _, err := loadRecord(ctx, d.opts.Store, tid)
	if err != nil {
		return err
	}
	if sec.PortToken == "" || rec.LinkURL == "" {
		p, err := d.opts.Client.PublishPrivatePort(ctx, m.ID, d.opts.LinkPort, "lydiabox-link")
		if err != nil {
			return d.fail(tid, err)
		}
		u, err := LinkURL(p.URL)
		if err != nil {
			return err
		}
		if rec, sec, err = d.update(ctx, tid, func(r *Record, s *recordSecrets) { r.LinkURL, s.PortToken = u, p.Token }); err != nil {
			return err
		}
	}
	restarted, err := d.prepareGuest(ctx, tid, m.ID, rec, sec)
	if err != nil {
		return err
	}
	d.startLease(tid, m.ID)
	l, err := d.links.For(tid)
	if err != nil {
		return err
	}
	target := link.Target{URL: rec.LinkURL, Header: http.Header{"Authorization": {"Bearer " + sec.PortToken}}}
	t := d.tenant(tid)
	d.mu.Lock()
	same := t.machineID == m.ID && t.attached
	t.machineID, t.attached, t.down, t.reported = m.ID, true, false, vmm.StateRunning
	d.mu.Unlock()
	if same && !restarted && l.Status().State != link.StateIdle {
		err = l.Wake()
	} else {
		err = l.Connect(target)
	}
	d.gate.record(tid, nil)
	return err
}

// prepareGuest upgrades the guest binary (upgrade.go), writes the guest
// settings, and upgrades the system files (system.go), then restarts the
// guest daemon once when the binary or the settings need it. It reports
// whether it restarted the daemon: the link must then connect again. The
// binary is installed before guest.env is written, so that the wrapper of
// a new machine starts the new binary at once. The system upgrade comes
// after the binary, so that it copies the new binary to the image path;
// it never needs a restart. One probe serves both upgrades.
func (d *Driver) prepareGuest(ctx context.Context, tid, id string, rec Record, sec recordSecrets) (bool, error) {
	hg := d.readHostGuest()
	needGuest := hg != nil && rec.GuestSHA256 != guestKey(id, hg.hash)
	sys := d.planSystem(id, hg, rec)
	var st *guestState
	if needGuest || sys != nil {
		var err error
		if st, err = d.probe(ctx, tid, id); err != nil {
			return false, err
		}
		if st == nil && sys != nil {
			d.noteSystemFailed(sys)
		}
	}
	gst := st
	if !needGuest {
		gst = nil
	}
	up, err := d.upgradeGuest(ctx, tid, id, hg, gst)
	if err != nil {
		return false, err
	}
	envHash, envRestart, err := d.ensureGuestEnv(ctx, tid, id, rec, sec)
	if err != nil {
		return false, err
	}
	sysKey := ""
	if st != nil {
		next := st.file
		if up.installed {
			next = hg.hash
		}
		if sysKey, err = d.upgradeSystem(ctx, tid, id, sys, st, next); err != nil {
			return false, err
		}
	}
	restarted := false
	if envRestart || up.restart {
		why := "guest settings changed"
		if !envRestart {
			why = "a new guest binary is installed and no terminal or harness session runs"
		}
		d.opts.Logger.Info("instabox: restarting the guest daemon: "+why, "tenant", tid, "machine", id)
		err := d.restartGuest(ctx, id)
		switch {
		case err == nil:
			restarted = true
		case envRestart:
			return false, d.fail(tid, err)
		default:
			var acc *AccountError
			if err = d.fail(tid, err); errors.As(err, &acc) {
				return false, err
			}
			d.opts.Logger.Warn("instabox: guest upgrade: restart; the new guest binary is staged until the next attach",
				"tenant", tid, "machine", id, "err", err)
		}
	}
	guestDone := up.done || (restarted && up.installed)
	if envHash == "" && !guestDone && sysKey == "" {
		return restarted, nil
	}
	_, _, err = d.update(ctx, tid, func(r *Record, _ *recordSecrets) {
		if envHash != "" {
			r.EnvSHA256 = envHash
		}
		if guestDone {
			r.GuestSHA256 = up.key
		}
		if sysKey != "" {
			r.SystemSHA256 = sysKey
		}
	})
	return restarted, err
}

// restartGuest restarts the guest daemon with the image's supervisor.
func (d *Driver) restartGuest(ctx context.Context, id string) error {
	res, err := d.opts.Client.Exec(ctx, id, ib.ExecRequest{Cmd: restartGuest, TimeoutSeconds: 60, NoHistory: true})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("instabox: restart of the guest daemon exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// ensureGuestEnv writes guest.env when the machine does not have the
// current one. It returns the hash to record ("" when the record has it
// already). The image's wrapper waits for a missing file; a file with
// other values (an adopted machine) needs a restart of the guest daemon.
func (d *Driver) ensureGuestEnv(ctx context.Context, tid, id string, rec Record, sec recordSecrets) (string, bool, error) {
	want := guestEnv(sec.LinkSecret, d.opts.LinkPort)
	sum := sha256.Sum256(append([]byte(id+"\x00"), want...))
	hash := hex.EncodeToString(sum[:])
	if rec.EnvSHA256 == hash {
		return "", false, nil
	}
	cur, err := d.opts.Client.ReadFile(ctx, id, guestEnvPath, 64<<10)
	restart := false
	switch {
	case err == nil && bytes.Equal(cur, want):
	case err == nil:
		restart = true
		fallthrough
	case errors.Is(err, ib.ErrNotFound):
		if _, err := d.opts.Client.PutFileBytes(ctx, id, guestEnvPath, want, ib.PutFileOptions{Mode: 0o600, Parents: true}); err != nil {
			return "", false, d.fail(tid, err)
		}
	default:
		return "", false, d.fail(tid, err)
	}
	return hash, restart, nil
}

// detach closes the link and ends the activity lease before a pause or a
// stop: a reconnect or a lease renewal would wake the machine again.
func (d *Driver) detach(tid string) {
	d.markDown(tid)
	if l, err := d.links.For(tid); err == nil {
		l.Suspend()
	}
}

// markDown records that the machine is not usable without a boot, and
// ends the lease renewal.
func (d *Driver) markDown(tid string) {
	t := d.tenant(tid)
	d.mu.Lock()
	t.attached, t.down = false, true
	lease := t.lease
	t.lease = nil
	d.mu.Unlock()
	if lease != nil {
		lease.stop(false)
	}
}

// leaseLoop renews the activity lease of one machine.
type leaseLoop struct {
	cancel context.CancelFunc
	done   chan struct{}
	del    func()
}

// stop ends the renewals; release also deletes the lease, so that the
// machine's idle pause applies from now on.
func (l *leaseLoop) stop(release bool) {
	l.cancel()
	<-l.done
	if release {
		l.del()
	}
}

// startLease holds the activity lease of a running machine while this
// process is attached to it.
func (d *Driver) startLease(tid, id string) {
	t := d.tenant(tid)
	d.mu.Lock()
	if t.lease != nil || d.closed {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := &leaseLoop{cancel: cancel, done: make(chan struct{}), del: func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		if err := d.opts.Client.DeleteActivityLease(dctx, id, LeaseID); err != nil {
			d.opts.Logger.Warn("instabox: release the activity lease", "tenant", tid, "err", err)
		}
	}}
	t.lease = l
	d.mu.Unlock()
	go func() {
		defer close(l.done)
		for {
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			_, err := d.opts.Client.PutActivityLease(rctx, id, LeaseID, d.opts.LeaseTTL)
			rcancel()
			if err != nil && ctx.Err() == nil {
				d.opts.Logger.Warn("instabox: renew the activity lease", "tenant", tid, "err", err)
				d.poke()
			}
			if sleep(ctx, d.opts.LeaseRenew) != nil {
				return
			}
		}
	}()
}

// linkState reacts to a link that went down without a detach: the
// machine may have paused itself, so the driver looks now, in the link's
// goroutine, before the link reconnects.
func (d *Driver) linkState(tid string, s link.Status) {
	d.noteLink(tid, s)
	if s.State != link.StateDown {
		return
	}
	if strings.Contains(s.LastError, "HTTP "+strconv.Itoa(http.StatusUnauthorized)) {
		d.opts.Logger.Warn("instabox: the port refused the token; the next attach publishes the port again", "tenant", tid)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _, err := d.update(ctx, tid, func(_ *Record, s *recordSecrets) { s.PortToken = "" })
		cancel()
		if err != nil {
			d.opts.Logger.Warn("instabox: clear the port token", "tenant", tid, "err", err)
		}
		t := d.tenant(tid)
		d.mu.Lock()
		t.attached = false
		d.mu.Unlock()
	}
	d.checkLink(tid)
}

// poke asks the watcher for an early round.
func (d *Driver) poke() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}
