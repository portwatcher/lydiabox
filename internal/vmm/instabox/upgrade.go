package instabox

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	instaboximage "github.com/lydiabox/lydiabox/deploy/instabox"
	ib "github.com/lydiabox/lydiabox/internal/instabox"
)

// Guest binary upgrade (docs/instabox.md, "Guest binary upgrade"). The
// image's wrapper (deploy/instabox/lydiabox-instabox-guest) runs
// guestOverridePath instead of guestImagePath when that file is
// executable. The driver puts the host's guest binary there when the
// machine has another one, so that a release of the control plane brings
// every machine to the guest of the host, with no new snapshot.
const (
	// guestImagePath is the guest binary of the image.
	guestImagePath = "/usr/local/bin/lydiabox-guest"
	// guestOverridePath is the guest binary on the persistent disk.
	guestOverridePath = "/data/.lydiabox/bin/lydiabox-guest"
	// guestUploadPath is the temporary name of an upload. The install
	// moves it into place only after its hash is correct, so the wrapper
	// never runs a part of a file.
	guestUploadPath = "/data/.lydiabox/bin/.lydiabox-guest.upload"
)

// probeMachine prints the state of the guest binary and of the system
// files in one exec:
//
//	file H    the SHA-256 of the binary that the wrapper starts next
//	image H   the SHA-256 of the binary of the image path (system.go)
//	system H  the content of the system marker ("" when it is missing)
//	env 0|1   0: guest.env is missing, so the wrapper waits and starts
//	          that binary later
//	daemon H  the SHA-256 of the binary of the running guest daemon
//	ptys N    the number of PTYs (terminals and harness sessions) of the
//	          running daemon
//
// A PTY is a child of the daemon with a controlling terminal. A restart
// of the daemon ends it. The desktop programs (their own sessions with no
// terminal) and the video encoder are not counted: a restart does not end
// the desktop programs, and the daemon starts the encoder again.
const probeMachine = `i=$(sha256sum ` + guestImagePath + ` 2>/dev/null | cut -c1-64)
o=` + guestOverridePath + `
f=$i
if [ -x "$o" ]; then f=$(sha256sum "$o" 2>/dev/null | cut -c1-64); fi
echo "file $f"
echo "image $i"
echo "system $(head -n 1 ` + instaboximage.MarkerPath + ` 2>/dev/null | cut -c1-64)"
if [ -r ` + guestEnvPath + ` ]; then echo "env 1"; else echo "env 0"; fi
p=$(pgrep -o -u 1000 -f 'lydiabox-guest [s]erve' || true)
if [ -n "$p" ]; then
	echo "daemon $(sha256sum /proc/$p/exe 2>/dev/null | cut -c1-64)"
	echo "ptys $(ps -e -o ppid= -o tty= | awk -v p="$p" '$1 == p && $2 != "?" { n++ } END { print n + 0 }')"
fi
`

// installGuest returns the command that moves a complete upload into
// place. hash is the hex SHA-256 that the driver computed.
func installGuest(hash string) string {
	return `set -e
t=` + guestUploadPath + `
if [ "$(sha256sum "$t" | cut -c1-64)" != "` + hash + `" ]; then
	rm -f "$t"
	echo "the uploaded guest binary does not have the expected SHA-256" >&2
	exit 3
fi
chmod 0755 "$t"
mv -f "$t" ` + guestOverridePath + `
`
}

// hostGuest is the guest binary of the host, read once for each process.
type hostGuest struct {
	path string
	hash string
}

// guestState is the answer of probeMachine.
type guestState struct {
	file   string
	image  string
	system string
	env    bool
	daemon bool
	// running is the hash of the daemon's binary ("" when unknown).
	running string
	ptys    int
}

func parseGuestState(out string) (guestState, error) {
	var st guestState
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		key, val, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		seen[key] = true
		switch key {
		case "file":
			st.file = val
		case "image":
			st.image = val
		case "system":
			st.system = val
		case "env":
			st.env = val == "1"
		case "daemon":
			st.daemon, st.running = true, val
		case "ptys":
			n, err := strconv.Atoi(val)
			if err != nil {
				return st, fmt.Errorf("instabox: machine probe: bad PTY count %q", val)
			}
			st.ptys = n
		}
	}
	if !seen["file"] || !seen["image"] || !seen["system"] || !seen["env"] || (st.daemon && !seen["ptys"]) {
		return st, fmt.Errorf("instabox: machine probe: unexpected output %q", out)
	}
	return st, nil
}

// readHostGuest hashes the host's guest binary once. It returns nil when
// there is none, or when it is not a linux/amd64 binary.
func (d *Driver) readHostGuest() *hostGuest {
	d.guestOnce.Do(func() {
		p := d.opts.GuestBinary
		if p == "" {
			return
		}
		f, err := os.Open(p)
		if err != nil {
			d.opts.Logger.Warn("instabox: cannot read the guest binary; no guest upgrade", "path", p, "err", err)
			return
		}
		defer f.Close()
		var hdr [20]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil || !amd64ELF(hdr[:]) {
			d.opts.Logger.Info("instabox: the guest binary is not a linux/amd64 executable; no guest upgrade", "path", p)
			return
		}
		h := sha256.New()
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			_, err = io.Copy(h, f)
		}
		if err != nil {
			d.opts.Logger.Warn("instabox: cannot read the guest binary; no guest upgrade", "path", p, "err", err)
			return
		}
		d.guest = &hostGuest{path: p, hash: hex.EncodeToString(h.Sum(nil))}
	})
	return d.guest
}

// amd64ELF reports whether hdr starts a 64-bit little-endian x86-64 ELF
// file. InstaBox machines are linux/amd64.
func amd64ELF(hdr []byte) bool {
	const emX8664 = 62
	return len(hdr) >= 20 && string(hdr[:4]) == "\x7fELF" && hdr[4] == 2 && hdr[5] == 1 &&
		binary.LittleEndian.Uint16(hdr[18:20]) == emX8664
}

// guestKey is the record value of a machine that runs the host's guest.
func guestKey(id, hash string) string {
	sum := sha256.Sum256([]byte(id + "\x00" + hash))
	return hex.EncodeToString(sum[:])
}

// guestUpgrade is the result of upgradeGuest.
type guestUpgrade struct {
	// key goes to Record.GuestSHA256 when done is true, or when a restart
	// of the daemon happens and installed is true.
	key       string
	done      bool
	installed bool
	// restart: the daemon runs an older binary and has no PTY.
	restart bool
}

// probe runs probeMachine. Only an account error (closed gate) is
// returned; on any other failure it logs one line and returns nil, and the
// upgrades wait for a later attach.
func (d *Driver) probe(ctx context.Context, tid, id string) (*guestState, error) {
	res, err := d.opts.Client.Exec(ctx, id, ib.ExecRequest{Cmd: probeMachine, TimeoutSeconds: 60, NoHistory: true})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	var st guestState
	if err == nil {
		st, err = parseGuestState(res.Stdout)
	}
	if err != nil {
		var acc *AccountError
		if err = d.fail(tid, err); errors.As(err, &acc) {
			return nil, err
		}
		d.opts.Logger.Warn("instabox: guest and system upgrade: probe; the machine keeps its guest and system files for now",
			"tenant", tid, "machine", id, "err", err)
		return nil, nil
	}
	return &st, nil
}

// upgradeGuest installs the host's guest binary on the machine when the
// machine has another one. st is the probe (nil: no probe; then it does
// nothing). It never restarts the daemon itself; it says if a restart is
// safe. Only an account error (closed gate) is returned: any other
// failure is logged, and the next attach tries again.
func (d *Driver) upgradeGuest(ctx context.Context, tid, id string, hg *hostGuest, st *guestState) (guestUpgrade, error) {
	if hg == nil || st == nil {
		return guestUpgrade{}, nil
	}
	up := guestUpgrade{key: guestKey(id, hg.hash)}
	warn := func(msg string, err error) (guestUpgrade, error) {
		var acc *AccountError
		if err = d.fail(tid, err); errors.As(err, &acc) {
			return guestUpgrade{}, err
		}
		d.opts.Logger.Warn("instabox: guest upgrade: "+msg+"; the machine keeps its guest until the next attach",
			"tenant", tid, "machine", id, "err", err)
		return guestUpgrade{}, nil
	}
	if st.file != hg.hash {
		if err := d.installGuest(ctx, id, hg); err != nil {
			return warn("install", err)
		}
		d.opts.Logger.Info("instabox: installed the guest binary of the host", "tenant", tid, "machine", id, "sha256", hg.hash[:12])
	}
	up.installed = true
	switch {
	case !st.daemon && !st.env:
		// The wrapper waits for guest.env and then starts the new binary.
		up.done = true
	case !st.daemon:
		// The daemon starts now or restarts after a crash: look again at
		// the next attach.
	case st.running == hg.hash:
		up.done = true
	case st.ptys == 0:
		up.restart = true
	default:
		d.opts.Logger.Info("instabox: the new guest binary is staged; it runs from the next start of the guest daemon "+
			"(terminals or harness sessions run now)", "tenant", tid, "machine", id, "ptys", st.ptys)
	}
	return up, nil
}

// installGuest uploads the host's binary to a temporary name and moves it
// into place.
func (d *Driver) installGuest(ctx context.Context, id string, hg *hostGuest) error {
	f, err := os.Open(hg.path)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := d.opts.Client.PutFile(ctx, id, guestUploadPath, f, ib.PutFileOptions{Mode: 0o755, Parents: true})
	if err != nil {
		return err
	}
	if w.SHA256 != "" && w.SHA256 != hg.hash {
		return fmt.Errorf("the upload has the SHA-256 %s, want %s (the guest binary changed on the host?)", w.SHA256, hg.hash)
	}
	res, err := d.opts.Client.Exec(ctx, id, ib.ExecRequest{Cmd: installGuest(hg.hash), TimeoutSeconds: 60, NoHistory: true})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("install exited %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}
