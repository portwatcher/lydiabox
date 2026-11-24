package docker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/vmm"
)

func writeGuest(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "lydiabox-guest")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func newPersistDriver(t *testing.T) (*Driver, *fakeDocker, vmm.Spec, string) {
	t.Helper()
	dir := t.TempDir()
	fd := &fakeDocker{imageID: "sha256:one"}
	d, err := New(Options{RunDir: dir, Runner: fd, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	spec := vmm.Spec{CPUs: 2, MemMB: 2048, HomeDir: "/jfs/home/agent", WorkspaceDir: "/jfs/workspace",
		GuestBinary: writeGuest(t, dir, "v1"), WebRTCPort: 50000}
	return d, fd, spec, dir
}

// cycle runs Create, Boot, and Shutdown like the controller does.
func cycle(t *testing.T, d *Driver, spec vmm.Spec) {
	t.Helper()
	ctx := context.Background()
	if err := d.Create(ctx, "default", spec); err != nil {
		t.Fatal(err)
	}
	if err := d.Boot(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if st, _ := d.State(ctx, "default"); st != vmm.StateRunning {
		t.Fatalf("state after boot %s", st)
	}
	if err := d.Shutdown(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeDocker) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestStopKeepsContainer(t *testing.T) {
	d, fd, spec, _ := newPersistDriver(t)
	cycle(t, d, spec)
	if fd.status != "exited" || fd.count("rm ") != 0 {
		t.Fatalf("container after stop: status %q, rm calls %d", fd.status, fd.count("rm "))
	}
	if fd.label == "" {
		t.Fatal("the container has no spec label")
	}
}

func TestBootStartsExistingContainer(t *testing.T) {
	d, fd, spec, _ := newPersistDriver(t)
	cycle(t, d, spec)
	cycle(t, d, spec)
	cycle(t, d, spec)
	if fd.creates != 1 || fd.count("rm ") != 0 || fd.count("start lydiabox-default") != 3 {
		t.Fatalf("creates %d, rm %d, start %d; calls %v", fd.creates, fd.count("rm "), fd.count("start "), fd.calls)
	}

	// A container left running (host crash) is stopped and reused.
	if err := d.Boot(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if err := d.Create(context.Background(), "default", spec); err != nil {
		t.Fatal(err)
	}
	if fd.creates != 1 || fd.status != "exited" {
		t.Fatalf("creates %d, status %q", fd.creates, fd.status)
	}
}

func TestRecreateOnChangedSpec(t *testing.T) {
	d, fd, spec, dir := newPersistDriver(t)
	cycle(t, d, spec)
	changes := []struct {
		name  string
		apply func()
	}{
		{"memory", func() { spec.MemMB = 4096 }},
		{"image tag", func() { spec.Image = "lydiabox/guest:slim" }},
		{"image rebuilt", func() { fd.imageID = "sha256:two" }},
		{"guest binary", func() { writeGuest(t, dir, "v2") }},
	}
	for i, c := range changes {
		before := fd.label
		c.apply()
		cycle(t, d, spec)
		if fd.creates != i+2 || fd.label == before {
			t.Fatalf("%s: creates %d, label %q (before %q)", c.name, fd.creates, fd.label, before)
		}
	}
	if fd.count("rm --force lydiabox-default") != len(changes) {
		t.Fatalf("rm calls %d", fd.count("rm --force"))
	}

	// ResetRoot is the explicit purge: the next Create makes a new container.
	if err := d.ResetRoot(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	cycle(t, d, spec)
	if fd.creates != len(changes)+2 {
		t.Fatalf("creates after reset %d", fd.creates)
	}
}

func TestMissingImageIsPulled(t *testing.T) {
	d, fd, spec, _ := newPersistDriver(t)
	fd.imageID = ""
	cycle(t, d, spec)
	if fd.count("pull lydiabox/guest:latest") != 1 {
		t.Fatalf("calls %v", fd.calls)
	}
}
