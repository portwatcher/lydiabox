//go:build unix

package instabox_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
	"github.com/portwatcher/lydiabox/internal/vmm"
	"github.com/portwatcher/lydiabox/internal/vmm/guestclient"
	vmmib "github.com/portwatcher/lydiabox/internal/vmm/instabox"
)

const (
	overridePath = "/data/.lydiabox/bin/lydiabox-guest"
	uploadPath   = "/data/.lydiabox/bin/.lydiabox-guest.upload"
	envPath      = "/data/.lydiabox/guest.env"
)

// guestImage plays the shell commands of the guest and system upgrades in
// the fake machines: the probe, the install, the restart, and the system
// run (system_test.go). The binary that a daemon runs is the one that the
// wrapper found when the daemon started.
type guestImage struct {
	e     *env
	image []byte // /usr/local/bin/lydiabox-guest of the snapshot

	mu      sync.Mutex
	running map[string]string // machine -> hash of the daemon's binary
	boots   map[string]int    // machine -> runner boots when running was set
	probes  int
	failMv  bool
	// The system state: the image path binary and the marker of each
	// machine (snapshot values when missing), and the events in order.
	imagePath  map[string]string
	marker     map[string]string
	snapMarker string
	failSystem bool
	events     []string
}

func newGuestImage(e *env, image []byte) *guestImage {
	g := &guestImage{e: e, image: image, running: map[string]string{}, boots: map[string]int{},
		imagePath: map[string]string{}, marker: map[string]string{}}
	e.mu.Lock()
	e.onExec = g.exec
	e.mu.Unlock()
	return g
}

func hashOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// next is the hash of the binary that the wrapper starts.
func (g *guestImage) next(id string) string {
	if b, mode, ok := g.e.fake.File(id, overridePath); ok && mode&0o100 != 0 {
		return hashOf(b)
	}
	return g.imageHash(id)
}

// imageHash is the hash of the image path binary of a machine.
func (g *guestImage) imageHash(id string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if h, ok := g.imagePath[id]; ok {
		return h
	}
	return hashOf(g.image)
}

// markerOf is the system marker of a machine.
func (g *guestImage) markerOf(id string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.marker[id]; ok {
		return m
	}
	return g.snapMarker
}

func (g *guestImage) event(s string) {
	g.mu.Lock()
	g.events = append(g.events, s)
	g.mu.Unlock()
}

// daemon returns the hash of the running daemon's binary, or "".
func (g *guestImage) daemon(id string) string {
	if _, _, ok := g.e.fake.File(id, envPath); !ok {
		return "" // the wrapper waits for guest.env
	}
	next := g.next(id)
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := g.e.runner.Boots(id); n != g.boots[id] {
		g.running[id], g.boots[id] = next, n
	}
	return g.running[id]
}

var hashArg = regexp.MustCompile(`"([0-9a-f]{64})"`)

func (g *guestImage) exec(id string, req instaboxtest.ExecRequest) (instaboxtest.ExecResult, bool) {
	switch {
	case strings.Contains(req.Cmd, "pgrep"):
		g.mu.Lock()
		g.probes++
		g.mu.Unlock()
		_, _, env := g.e.fake.File(id, envPath)
		out := fmt.Sprintf("file %s\nimage %s\nsystem %s\nenv %d\n", g.next(id), g.imageHash(id), g.markerOf(id),
			map[bool]int{false: 0, true: 1}[env])
		if d := g.daemon(id); d != "" {
			n := 0
			for _, p := range g.e.runner.PTYs(id) {
				if p.Running {
					n++
				}
			}
			out += fmt.Sprintf("daemon %s\nptys %d\n", d, n)
		}
		return instaboxtest.ExecResult{Stdout: out}, true
	case strings.Contains(req.Cmd, "lydiabox-system"):
		return g.runSystem(id, req), true
	case strings.Contains(req.Cmd, "mv -f"):
		g.event("install guest")
		m := hashArg.FindStringSubmatch(req.Cmd)
		b, _, ok := g.e.fake.File(id, uploadPath)
		g.mu.Lock()
		fail := g.failMv
		g.mu.Unlock()
		if fail || m == nil || !ok || hashOf(b) != m[1] {
			return instaboxtest.ExecResult{ExitCode: 3, Stderr: "bad upload\n"}, true
		}
		g.e.fake.RenameFile(id, uploadPath, overridePath)
		return instaboxtest.ExecResult{}, true
	case strings.Contains(req.Cmd, "restart lydiabox-guest"):
		g.e.runner.Restart(id)
		g.daemon(id)
		return instaboxtest.ExecResult{Stdout: "lydiabox-guest: started\n"}, true
	}
	return instaboxtest.ExecResult{}, false
}

func (g *guestImage) probeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.probes
}

// elfFile writes a fake ELF executable for machine (62 x86-64, 183
// arm64) and returns its path and content.
func elfFile(t *testing.T, machine uint16, payload string) (string, []byte) {
	t.Helper()
	hdr := make([]byte, 64)
	copy(hdr, "\x7fELF")
	hdr[4], hdr[5], hdr[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(hdr[16:], 2)
	binary.LittleEndian.PutUint16(hdr[18:], machine)
	data := append(hdr, payload...)
	p := filepath.Join(t.TempDir(), "lydiabox-guest")
	if err := os.WriteFile(p, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return p, data
}

// logBuf is a log sink that the driver's goroutines may write to.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func withGuest(path string, log *logBuf) func(*vmmib.Options) {
	return func(o *vmmib.Options) {
		o.GuestBinary = path
		if log != nil {
			o.Logger = slogText(log)
		}
	}
}

func slogText(log *logBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(log, nil))
}

func (e *env) restarts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, c := range e.cmds {
		if strings.Contains(c, "restart lydiabox-guest") {
			n++
		}
	}
	return n
}

func (e *env) uploads(id string) int {
	return e.count(http.MethodPut, "/v1/machines/"+id+"/files")
}

func guestRecord(t *testing.T, e *env) string {
	t.Helper()
	rec, _, err := vmmib.LoadRecord(context.Background(), e.store, tenant)
	if err != nil {
		t.Fatal(err)
	}
	return rec.GuestSHA256
}

func keyFor(id string, bin []byte) string {
	return hashOf([]byte(id + "\x00" + hashOf(bin)))
}

// checkOverride verifies that the machine has bin in place and no upload
// left behind.
func checkOverride(t *testing.T, e *env, id string, bin []byte) {
	t.Helper()
	got, mode, ok := e.fake.File(id, overridePath)
	if !ok || !bytes.Equal(got, bin) || mode != 0o755 {
		t.Fatalf("override: ok %v, mode %o, same %v", ok, mode, bytes.Equal(got, bin))
	}
	if _, _, ok := e.fake.File(id, uploadPath); ok {
		t.Fatal("the upload was left behind")
	}
}

func TestGuestUpgradeSameBinaryCostsOneProbe(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	bin, data := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, data)
	d := e.driver(t, withGuest(bin, nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	if g.probeCount() != 1 || e.uploads(id) != 1 || e.restarts() != 0 {
		t.Fatalf("probes %d, uploads %d (guest.env only), restarts %d", g.probeCount(), e.uploads(id), e.restarts())
	}
	if _, _, ok := e.fake.File(id, overridePath); ok {
		t.Fatal("the same binary was uploaded")
	}
	if guestRecord(t, e) != keyFor(id, data) {
		t.Fatal("the record does not have the guest binary")
	}
	// A new process with the same binary: no API call for the upgrade.
	d.Close()
	before := len(e.fake.Requests())
	d2 := e.driver(t, withGuest(bin, nil))
	ping(t, d2)
	if g.probeCount() != 1 {
		t.Fatalf("%d probes, want 1", g.probeCount())
	}
	for _, r := range e.fake.Requests()[before:] {
		if strings.HasSuffix(r.Path, "/exec") || strings.HasSuffix(r.Path, "/files") {
			t.Fatalf("unexpected request %s %s", r.Method, r.Path)
		}
	}
}

func TestGuestUpgradeOnNewMachineNeedsNoRestart(t *testing.T) {
	e := newEnv(t)
	_, old := elfFile(t, 62, "guest v1")
	bin, data := elfFile(t, 62, "guest v2")
	g := newGuestImage(e, old)
	d := e.driver(t, withGuest(bin, nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	checkOverride(t, e, id, data)
	if e.restarts() != 0 {
		t.Fatalf("%d restarts of a daemon that was not running", e.restarts())
	}
	if got := g.daemon(id); got != hashOf(data) {
		t.Fatal("the daemon does not run the new binary")
	}
	if guestRecord(t, e) != keyFor(id, data) {
		t.Fatal("the record does not have the guest binary")
	}
}

func TestGuestUpgradeRestartsAStartedMachine(t *testing.T) {
	e := newEnv(t)
	oldBin, old := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, old)
	d := e.driver(t, withGuest(oldBin, nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	ctx := context.Background()
	if err := d.Shutdown(ctx, tenant, false); err != nil {
		t.Fatal(err)
	}
	d.Close()

	// A release with a new guest; the stopped machine starts again.
	bin, data := elfFile(t, 62, "guest v2")
	d2 := e.driver(t, withGuest(bin, nil))
	if err := d2.Boot(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	ping(t, d2)
	checkOverride(t, e, id, data)
	if e.restarts() != 1 {
		t.Fatalf("%d restarts, want 1", e.restarts())
	}
	if g.daemon(id) != hashOf(data) {
		t.Fatal("the daemon does not run the new binary")
	}
	if guestRecord(t, e) != keyFor(id, data) {
		t.Fatal("the record does not have the new guest binary")
	}
}

func TestGuestUpgradeKeepsLiveSessions(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	oldBin, old := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, old)
	d := e.driver(t, withGuest(oldBin, nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gc := guestclient.New(guestclient.Options{Dialer: d, Logger: slog.New(slog.DiscardHandler)})
	pty, err := gc.StartPTY(ctx, tenant, guestclient.PTYSpec{Command: []string{"sleep", "60"}})
	if err != nil {
		t.Fatal(err)
	}
	ptyID := pty.ID()
	pty.Close()
	gc.Reset(tenant)
	d.Close()
	boots := e.runner.Boots(id)

	// A new process with a new guest attaches to the running machine:
	// the binary is staged, the terminal runs on.
	bin, data := elfFile(t, 62, "guest v2")
	log := &logBuf{}
	d2 := e.driver(t, withGuest(bin, log))
	ping(t, d2)
	checkOverride(t, e, id, data)
	if e.restarts() != 0 || e.runner.Boots(id) != boots {
		t.Fatalf("the daemon restarted with a live terminal: %d restarts", e.restarts())
	}
	if !strings.Contains(log.String(), "staged") {
		t.Fatalf("no staged line in the log:\n%s", log.String())
	}
	if guestRecord(t, e) == keyFor(id, data) {
		t.Fatal("a staged binary was recorded as running")
	}
	running := false
	for _, p := range e.runner.PTYs(id) {
		running = running || (p.ID == ptyID && p.Running)
	}
	if !running {
		t.Fatal("the terminal ended")
	}

	// The terminal ends; the next attach restarts the idle daemon and does
	// not upload again.
	gc2 := guestclient.New(guestclient.Options{Dialer: d2, Logger: slog.New(slog.DiscardHandler)})
	if err := gc2.KillPTY(ctx, tenant, ptyID); err != nil {
		t.Fatal(err)
	}
	gc2.Reset(tenant)
	d2.Close()
	uploads := e.uploads(id)
	d3 := e.driver(t, withGuest(bin, nil))
	ping(t, d3)
	if e.restarts() != 1 || e.uploads(id) != uploads {
		t.Fatalf("restarts %d (want 1), uploads %d (want %d)", e.restarts(), e.uploads(id), uploads)
	}
	if g.daemon(id) != hashOf(data) || guestRecord(t, e) != keyFor(id, data) {
		t.Fatal("the idle daemon does not run the new binary")
	}
}

func TestGuestUpgradeFailureDoesNotStopTheAttach(t *testing.T) {
	e := newEnv(t)
	oldBin, old := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, old)
	d := e.driver(t, withGuest(oldBin, nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	d.Close()

	bin, data := elfFile(t, 62, "guest v2")
	e.fake.InjectError(http.MethodPut, "/v1/machines/"+id+"/files", http.StatusInternalServerError, "internal", false, 0, 1)
	log := &logBuf{}
	d2 := e.driver(t, withGuest(bin, log), func(o *vmmib.Options) { o.Client = e.clientWith(t, -1, nil) })
	ping(t, d2)
	if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "guest upgrade") {
		t.Fatalf("no WARN line:\n%s", log.String())
	}
	if _, _, ok := e.fake.File(id, overridePath); ok || guestRecord(t, e) == keyFor(id, data) {
		t.Fatal("a failed upload was installed or recorded")
	}
	// A failed install (a bad hash) is not recorded either.
	g.mu.Lock()
	g.failMv = true
	g.mu.Unlock()
	if err := d2.Boot(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := e.fake.File(id, overridePath); ok || guestRecord(t, e) == keyFor(id, data) {
		t.Fatal("a failed install was installed or recorded")
	}
	g.mu.Lock()
	g.failMv = false
	g.mu.Unlock()
	// The next attach tries again.
	if err := d2.Boot(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	ping(t, d2)
	checkOverride(t, e, id, data)
	if guestRecord(t, e) != keyFor(id, data) {
		t.Fatal("the retry was not recorded")
	}
}

func TestGuestUpgradeSkipsOtherArchitectures(t *testing.T) {
	e := newEnv(t)
	_, old := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, old)
	bin, _ := elfFile(t, 183, "guest v2 arm64")
	log := &logBuf{}
	d := e.driver(t, withGuest(bin, log))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	if g.probeCount() != 0 {
		t.Fatalf("%d probes for an arm64 binary", g.probeCount())
	}
	if _, _, ok := e.fake.File(id, overridePath); ok {
		t.Fatal("an arm64 binary was uploaded")
	}
	if n := strings.Count(log.String(), "not a linux/amd64"); n != 1 {
		t.Fatalf("%d INFO lines about the architecture, want 1:\n%s", n, log.String())
	}
	if st := state(t, d); st != vmm.StateRunning {
		t.Fatalf("state %s", st)
	}
}
