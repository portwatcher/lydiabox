//go:build unix

package instabox_test

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"

	instaboximage "github.com/lydiabox/lydiabox/deploy/instabox"
	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

const systemUpload = "/data/.lydiabox/.system-bundle.upload"

var systemArg = regexp.MustCompile(`lydiabox-system "\$u" ([0-9a-f]{64})`)

// runSystem plays the root exec of the system upgrade: it checks the
// upload, removes it, and, unless failSystem is set, writes the marker and
// copies the executable override to the image path.
func (g *guestImage) runSystem(id string, req instaboxtest.ExecRequest) instaboxtest.ExecResult {
	g.event("system")
	m := systemArg.FindStringSubmatch(req.Cmd)
	b, _, ok := g.e.fake.File(id, systemUpload)
	g.e.fake.RemoveFile(id, systemUpload)
	g.mu.Lock()
	fail := g.failSystem
	g.mu.Unlock()
	switch {
	case m == nil || !ok || hashOf(b) != m[1]:
		return instaboxtest.ExecResult{ExitCode: 3, Stderr: "the uploaded system bundle does not have the expected SHA-256\n"}
	case fail:
		return instaboxtest.ExecResult{ExitCode: 1, Stderr: "ERROR: apt-get update failed; the packages bubblewrap are not installed\n"}
	}
	next := g.next(id)
	g.mu.Lock()
	g.marker[id], g.imagePath[id] = m[1], next
	g.mu.Unlock()
	return instaboxtest.ExecResult{Stdout: "changed: /usr/local/bin/lbx -> /usr/local/bin/lydiabox-guest\n"}
}

func (g *guestImage) eventList() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.events...)
}

func (g *guestImage) systemRuns() int {
	n := 0
	for _, e := range g.eventList() {
		if e == "system" {
			n++
		}
	}
	return n
}

func bundle(t *testing.T) instaboximage.Bundle {
	t.Helper()
	b, err := instaboximage.System()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func withSystem(log *logBuf) func(*vmmib.Options) {
	return func(o *vmmib.Options) {
		o.NoSystemUpgrade = false
		if log != nil {
			o.Logger = slogText(log)
		}
	}
}

func systemRecord(t *testing.T, e *env) string {
	t.Helper()
	rec, _, err := vmmib.LoadRecord(context.Background(), e.store, tenant)
	if err != nil {
		t.Fatal(err)
	}
	return rec.SystemSHA256
}

func systemKey(id, bundle, guest string) string {
	return hashOf([]byte(id + "\x00" + bundle + "\x00" + guest))
}

// noUpgradeCalls fails when a request after before is an exec or a file
// upload.
func noUpgradeCalls(t *testing.T, e *env, before int) {
	t.Helper()
	for _, r := range e.fake.Requests()[before:] {
		if strings.HasSuffix(r.Path, "/exec") || strings.HasSuffix(r.Path, "/files") {
			t.Fatalf("unexpected request %s %s", r.Method, r.Path)
		}
	}
}

func TestSystemUpgradeCurrentMarkerCostsOneProbe(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	_, img := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, img)
	b := bundle(t)
	g.snapMarker = b.SHA256
	// No guest binary option: the system upgrade runs all the same.
	d := e.driver(t, withSystem(nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	if g.probeCount() != 1 || g.systemRuns() != 0 || e.uploads(id) != 1 {
		t.Fatalf("probes %d, system runs %d, uploads %d (guest.env only)", g.probeCount(), g.systemRuns(), e.uploads(id))
	}
	if systemRecord(t, e) != systemKey(id, b.SHA256, "") {
		t.Fatal("the record does not have the system bundle")
	}
	d.Close()
	before := len(e.fake.Requests())
	d2 := e.driver(t, withSystem(nil))
	ping(t, d2)
	if g.probeCount() != 1 {
		t.Fatalf("%d probes, want 1", g.probeCount())
	}
	noUpgradeCalls(t, e, before)
}

func TestSystemUpgradeInstallsTheBundle(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	_, img := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, img)
	g.snapMarker = strings.Repeat("0", 64) // an older bundle
	log := &logBuf{}
	d := e.driver(t, withSystem(log))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	b := bundle(t)
	if g.systemRuns() != 1 || g.markerOf(id) != b.SHA256 {
		t.Fatalf("system runs %d, marker %q", g.systemRuns(), g.markerOf(id))
	}
	if _, _, ok := e.fake.File(id, systemUpload); ok {
		t.Fatal("the upload was left behind")
	}
	if systemRecord(t, e) != systemKey(id, b.SHA256, "") {
		t.Fatal("the record does not have the system bundle")
	}
	if !strings.Contains(log.String(), "upgraded the system files of the machine") ||
		!strings.Contains(log.String(), "sha256="+b.SHA256[:12]) || !strings.Contains(log.String(), "/usr/local/bin/lbx") {
		t.Fatalf("no INFO line:\n%s", log.String())
	}
	if e.restarts() != 0 {
		t.Fatalf("%d restarts of the guest daemon", e.restarts())
	}
	d.Close()
	before := len(e.fake.Requests())
	d2 := e.driver(t, withSystem(nil))
	ping(t, d2)
	noUpgradeCalls(t, e, before)
}

func TestSystemUpgradeUploadIsTheBundle(t *testing.T) {
	e := newEnv(t)
	_, img := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, img)
	b := bundle(t)
	var got []byte
	e.mu.Lock()
	e.onExec = func(id string, req instaboxtest.ExecRequest) (instaboxtest.ExecResult, bool) {
		if strings.Contains(req.Cmd, "lydiabox-system") {
			got, _, _ = e.fake.File(id, systemUpload)
		}
		return g.exec(id, req)
	}
	e.mu.Unlock()
	d := e.driver(t, withSystem(nil))
	boot(t, d)
	ping(t, d)
	if !bytes.Equal(got, b.Tar) {
		t.Fatalf("the upload has %d bytes, the bundle %d", len(got), len(b.Tar))
	}
}

func TestSystemUpgradeFailureIsNotRetriedInTheProcess(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	_, img := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, img)
	g.failSystem = true
	log := &logBuf{}
	d := e.driver(t, withSystem(log))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	if !strings.Contains(log.String(), "level=WARN") || !strings.Contains(log.String(), "system upgrade: run") ||
		!strings.Contains(log.String(), "apt-get update failed") {
		t.Fatalf("no WARN line with the script output:\n%s", log.String())
	}
	if systemRecord(t, e) != "" || g.markerOf(id) != "" {
		t.Fatal("a failed system upgrade was recorded")
	}
	// The next attach of the same process does not try again.
	for range 2 {
		if err := d.Boot(context.Background(), tenant); err != nil {
			t.Fatal(err)
		}
	}
	if g.systemRuns() != 1 || g.probeCount() != 1 {
		t.Fatalf("system runs %d, probes %d; want 1 and 1", g.systemRuns(), g.probeCount())
	}
	// A failed upload is not retried either.
	d.Close()
	e.fake.InjectError(http.MethodPut, "/v1/machines/"+id+"/files", http.StatusInternalServerError, "internal", false, 0, 1)
	d2 := e.driver(t, withSystem(nil), func(o *vmmib.Options) { o.Client = e.clientWith(t, -1, nil) })
	ping(t, d2)
	if err := d2.Boot(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	if g.systemRuns() != 1 || g.probeCount() != 2 {
		t.Fatalf("system runs %d, probes %d; want 1 and 2", g.systemRuns(), g.probeCount())
	}
	// A later process tries again.
	d2.Close()
	g.mu.Lock()
	g.failSystem = false
	g.mu.Unlock()
	d3 := e.driver(t, withSystem(nil))
	ping(t, d3)
	if g.systemRuns() != 2 || systemRecord(t, e) != systemKey(id, bundle(t).SHA256, "") {
		t.Fatalf("system runs %d; record %q", g.systemRuns(), systemRecord(t, e))
	}
}

func TestSystemUpgradeRunsAfterTheGuestInstall(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	_, old := elfFile(t, 62, "guest v1")
	g := newGuestImage(e, old)
	b := bundle(t)
	// The marker is current: only the image path binary is old after the
	// guest install.
	g.snapMarker = b.SHA256
	bin, data := elfFile(t, 62, "guest v2")
	d := e.driver(t, withGuest(bin, nil), withSystem(nil))
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	if got := strings.Join(g.eventList(), ","); got != "install guest,system" {
		t.Fatalf("events %s, want the guest install, then the system run", got)
	}
	if g.probeCount() != 1 {
		t.Fatalf("%d probes, want 1 for both upgrades", g.probeCount())
	}
	if g.imageHash(id) != hashOf(data) {
		t.Fatal("the image path does not have the new guest binary")
	}
	if systemRecord(t, e) != systemKey(id, b.SHA256, hashOf(data)) || guestRecord(t, e) != keyFor(id, data) {
		t.Fatal("the record does not have the guest binary and the system bundle")
	}

	// A release with another guest binary: the bundle is the same, but the
	// image path gets the new binary again.
	d.Close()
	bin3, data3 := elfFile(t, 62, "guest v3")
	d2 := e.driver(t, withGuest(bin3, nil), withSystem(nil))
	ping(t, d2)
	if g.systemRuns() != 2 || g.imageHash(id) != hashOf(data3) {
		t.Fatalf("system runs %d; the image path has the new binary: %v", g.systemRuns(), g.imageHash(id) == hashOf(data3))
	}
	if systemRecord(t, e) != systemKey(id, b.SHA256, hashOf(data3)) {
		t.Fatal("the record does not have the new guest binary")
	}
}
