package instabox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	instaboximage "github.com/portwatcher/lydiabox/deploy/instabox"
	ib "github.com/portwatcher/lydiabox/internal/instabox"
)

// System upgrade (docs/instabox.md, "System upgrade"). InstaBox has no
// operation that replaces the root disk and keeps /data, so a machine
// keeps the system files of its snapshot. The driver installs the system
// bundle of this build (deploy/instabox/system.go) when the machine's
// marker names another bundle, or when the guest binary of the image path
// is not the binary that the wrapper starts.
const (
	// systemUploadPath is the temporary name of the bundle on the machine.
	systemUploadPath = "/data/.lydiabox/.system-bundle.upload"
	// systemTimeout bounds the root exec: it can run apt-get.
	systemTimeout = 5 * time.Minute
	// systemOutputMax is the end of the script output that a log line
	// keeps.
	systemOutputMax = 2000
)

// systemRoot is the part of runSystem that runs as root ($1: the upload,
// $2: the expected SHA-256). It copies the upload into a new directory
// that only root can write (mktemp -d below /run), checks the hash of that
// copy, unpacks it, and runs the script. It writes the marker only when
// the script succeeds, and it removes its directory in every case. On
// success it prints the "changed: " lines of the script; on failure the
// last lines of its output go to stderr. It has no single quote, so that
// runSystem can quote it.
const systemRoot = `set -u
u=$1
want=$2
d=$(mktemp -d /run/lydiabox-system.XXXXXXXX) || { echo "cannot make a temporary directory below /run" >&2; exit 1; }
rc=0
cp "$u" "$d/bundle.tar" || rc=1
if [ "$rc" -eq 0 ] && [ "$(sha256sum "$d/bundle.tar" | cut -c1-64)" != "$want" ]; then
	echo "the uploaded system bundle does not have the expected SHA-256" >&2
	rc=3
fi
if [ "$rc" -eq 0 ]; then
	mkdir "$d/b" && tar -xf "$d/bundle.tar" -C "$d/b" --no-same-owner || rc=1
fi
if [ "$rc" -eq 0 ]; then
	/bin/sh "$d/b/` + instaboximage.ScriptName + `" "$d/b" >"$d/log" 2>&1 || rc=$?
	if [ "$rc" -eq 0 ]; then
		m=` + instaboximage.MarkerPath + `
		[ "$(cat "$m" 2>/dev/null)" = "$want" ] || { mkdir -p "$(dirname "$m")" && echo "$want" >"$m.lydiabox-new" && chmod 0644 "$m.lydiabox-new" && mv -f "$m.lydiabox-new" "$m"; } || rc=1
	fi
	if [ "$rc" -eq 0 ]; then
		grep "^changed: " "$d/log" || true
	else
		tail -n 30 "$d/log" >&2
	fi
fi
rm -rf "$d"
exit "$rc"
`

// runSystem returns the command that installs the uploaded bundle. It
// runs as the machine user; only systemRoot runs with sudo. The upload is
// removed in every case.
func runSystem(hash string) string {
	return `u=` + systemUploadPath + `
sudo -n /bin/sh -c '` + systemRoot + `' lydiabox-system "$u" ` + hash + `
rc=$?
rm -f "$u"
exit $rc
`
}

// systemPlan is a system upgrade that the record does not show as done.
type systemPlan struct {
	bundle instaboximage.Bundle
	// guest is the SHA-256 of the host's guest binary ("" when the driver
	// has none).
	guest string
	// key goes to Record.SystemSHA256 when the machine has the bundle and
	// its image path has the guest binary that the wrapper starts.
	key string
}

// systemKey is the record value of a machine with the bundle and the
// guest binary guest on the image path.
func systemKey(id, bundle, guest string) string {
	sum := sha256.Sum256([]byte(id + "\x00" + bundle + "\x00" + guest))
	return hex.EncodeToString(sum[:])
}

// systemBundle returns the bundle that the driver installs, or nil when
// the system upgrade is off.
func (d *Driver) systemBundle() *instaboximage.Bundle {
	d.systemOnce.Do(func() {
		switch {
		case d.opts.NoSystemUpgrade:
		case d.opts.SystemBundle != nil:
			d.system = d.opts.SystemBundle
		default:
			b, err := instaboximage.System()
			if err != nil {
				d.opts.Logger.Warn("instabox: cannot make the system bundle; no system upgrade", "err", err)
				return
			}
			d.system = &b
		}
	})
	return d.system
}

// planSystem returns the system upgrade of a machine, or nil when the
// record shows it as done or when it failed before in this process.
func (d *Driver) planSystem(id string, hg *hostGuest, rec Record) *systemPlan {
	b := d.systemBundle()
	if b == nil {
		return nil
	}
	p := &systemPlan{bundle: *b}
	if hg != nil {
		p.guest = hg.hash
	}
	p.key = systemKey(id, b.SHA256, p.guest)
	if rec.SystemSHA256 == p.key {
		return nil
	}
	d.mu.Lock()
	failed := d.systemFailed[p.key]
	d.mu.Unlock()
	if failed {
		return nil
	}
	return p
}

// noteSystemFailed stops the attempts of this process for a plan: a
// failure (for example apt-get) can take minutes, and each attach would
// try again. A later process tries again.
func (d *Driver) noteSystemFailed(p *systemPlan) {
	d.mu.Lock()
	d.systemFailed[p.key] = true
	d.mu.Unlock()
}

// upgradeSystem installs the system bundle when the probe st shows another
// marker, or an image path guest binary that is not next (the binary that
// the wrapper starts after the guest step). It returns the key to record
// ("" for none). Only an account error (closed gate) is returned: any
// other failure is logged, and a later process tries again.
func (d *Driver) upgradeSystem(ctx context.Context, tid, id string, p *systemPlan, st *guestState, next string) (string, error) {
	if p == nil || st == nil {
		return "", nil
	}
	key := p.key
	if p.guest != "" && next != p.guest {
		// The guest step did not install the host's binary: do the system
		// step, but record nothing, so that the next attach looks again.
		key = ""
	}
	if st.system == p.bundle.SHA256 && st.image == next {
		return key, nil
	}
	short := p.bundle.SHA256[:12]
	if _, err := d.opts.Client.PutFileBytes(ctx, id, systemUploadPath, p.bundle.Tar, ib.PutFileOptions{Mode: 0o600, Parents: true}); err != nil {
		return "", d.systemWarn(tid, id, p, "upload", err)
	}
	// The script can run apt-get: a cancel of the caller must not stop it
	// in the middle.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemTimeout+2*time.Minute)
	defer cancel()
	res, err := d.opts.Client.Exec(rctx, id, ib.ExecRequest{
		Cmd: runSystem(p.bundle.SHA256), TimeoutSeconds: int(systemTimeout / time.Second), NoHistory: true,
	})
	if err != nil {
		return "", d.systemWarn(tid, id, p, "run", err)
	}
	if res.ExitCode != 0 || res.TimedOut {
		return "", d.systemWarn(tid, id, p, "run", fmt.Errorf("exit %d (timed out: %v): %s",
			res.ExitCode, res.TimedOut, lastOutput(res.Stderr)))
	}
	var changes []string
	for _, l := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if c, ok := strings.CutPrefix(l, "changed: "); ok {
			changes = append(changes, c)
		}
	}
	d.opts.Logger.Info("instabox: upgraded the system files of the machine; a new entry point or supervisord configuration "+
		"takes effect at the next start of the machine", "tenant", tid, "machine", id, "sha256", short,
		"changes", strings.Join(changes, "; "))
	return key, nil
}

// systemWarn logs a failed system upgrade and stops the attempts of this
// process. It returns only an account error.
func (d *Driver) systemWarn(tid, id string, p *systemPlan, msg string, err error) error {
	var acc *AccountError
	if err = d.fail(tid, err); errors.As(err, &acc) {
		return err
	}
	d.noteSystemFailed(p)
	d.opts.Logger.Warn("instabox: system upgrade: "+msg+"; the machine keeps its system files until a later start of the cell",
		"tenant", tid, "machine", id, "sha256", p.bundle.SHA256[:12], "err", err)
	return nil
}

// lastOutput returns the end of a command output for a log line.
func lastOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > systemOutputMax {
		s = "..." + s[len(s)-systemOutputMax:]
	}
	return s
}
