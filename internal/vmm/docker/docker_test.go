package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// fakeDocker emulates the docker CLI state machine.
type fakeDocker struct {
	mu     sync.Mutex
	calls  []string
	status string // "" means no container
}

func (f *fakeDocker) Run(_ context.Context, c storage.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(c.Args, " "))
	noSuch := errors.New("Error response from daemon: No such container: lydiabox-default")
	switch c.Args[0] {
	case "create":
		f.status = "created"
	case "start", "unpause":
		f.status = "running"
	case "pause":
		f.status = "paused"
	case "stop", "kill":
		f.status = "exited"
	case "rm":
		if f.status == "" {
			return noSuch
		}
		f.status = ""
	case "inspect":
		if f.status == "" {
			return noSuch
		}
		fmt.Fprintln(c.Stdout, f.status)
	}
	return nil
}

func (f *fakeDocker) Start(storage.Command) (storage.Process, error) {
	return nil, errors.New("not used")
}

func (f *fakeDocker) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func TestDockerLifecycleAndSockets(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	fd := &fakeDocker{}
	d, err := New(Options{RunDir: dir, Runner: fd, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if st, err := d.State(ctx, "default"); err != nil || st != vmm.StateStopped {
		t.Fatalf("state %s %v", st, err)
	}
	spec := vmm.Spec{CPUs: 2, MemMB: 4096, HomeDir: "/jfs/home/agent", WorkspaceDir: "/jfs/workspace",
		GuestBinary: "/opt/lydiabox/lydiabox-guest", WebRTCPort: 50000}
	if err := d.Create(ctx, "default", spec); err != nil {
		t.Fatal(err)
	}
	sockDir := filepath.Join(dir, "vm", "default", "sock")
	create := fd.last()
	for _, w := range []string{
		"create --name lydiabox-default ", "--cpus 2", "--memory 4096m",
		"--env LYDIABOX_GUEST_LISTEN=unix:///run/lydiabox/guest.sock",
		"--env LYDIABOX_HOST_ADDR=unix:///run/lydiabox/host-9001.sock",
		"--env LYDIABOX_GATEWAY_ADDR=unix:///run/lydiabox/host-9002.sock",
		"--volume " + sockDir + ":/run/lydiabox", "--volume /jfs/home/agent:/home/agent", "--volume /jfs/workspace:/workspace",
		"--volume /opt/lydiabox/lydiabox-guest:/usr/local/bin/lydiabox-guest:ro", "--publish 0.0.0.0:50000:50000/udp",
	} {
		if !strings.Contains(create, w) {
			t.Fatalf("missing %q in %s", w, create)
		}
	}
	if !strings.HasSuffix(create, " "+DefaultImage) {
		t.Fatalf("image: %s", create)
	}
	if fi, err := os.Stat(sockDir); err != nil || fi.Mode().Perm() != 0o777 {
		t.Fatalf("socket dir %v %v", fi, err)
	}

	steps := []struct {
		fn   func(context.Context, string) error
		cmd  string
		want vmm.State
	}{
		{d.Boot, "start lydiabox-default", vmm.StateRunning},
		{d.Pause, "pause lydiabox-default", vmm.StatePaused},
		{d.Resume, "unpause lydiabox-default", vmm.StateRunning},
	}
	for _, s := range steps {
		if err := s.fn(ctx, "default"); err != nil {
			t.Fatal(err)
		}
		if fd.last() != s.cmd {
			t.Fatalf("command %q, want %q", fd.last(), s.cmd)
		}
		if st, _ := d.State(ctx, "default"); st != s.want {
			t.Fatalf("state %s, want %s", st, s.want)
		}
	}

	// The guest daemon socket and a guest-to-host port.
	gl, err := net.Listen("unix", filepath.Join(sockDir, "guest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer gl.Close()
	go func() {
		c, err := gl.Accept()
		if err == nil {
			_, _ = io.WriteString(c, "hi")
			c.Close()
		}
	}()
	conn, err := d.DialGuest(ctx, "default", 9000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(conn)
	conn.Close()
	if string(b) != "hi" {
		t.Fatalf("guest said %q", b)
	}
	hl, err := d.ListenGuestPort(ctx, "default", 9001)
	if err != nil {
		t.Fatal(err)
	}
	defer hl.Close()
	if hl.Addr().String() != filepath.Join(sockDir, "host-9001.sock") {
		t.Fatalf("listener %s", hl.Addr())
	}

	if err := d.Shutdown(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	fd.mu.Lock()
	tail := strings.Join(fd.calls[len(fd.calls)-3:], ",")
	fd.mu.Unlock()
	if tail != "inspect --format {{.State.Status}} lydiabox-default,stop --time 30 lydiabox-default,rm --force lydiabox-default" {
		t.Fatalf("shutdown calls %s", tail)
	}
	if st, _ := d.State(ctx, "default"); st != vmm.StateStopped {
		t.Fatalf("state %s", st)
	}
	if err := d.Shutdown(ctx, "default", true); err != nil {
		t.Fatalf("shutdown of a missing container: %v", err)
	}
	if _, err := d.DialGuest(ctx, "a/b", 9000); err == nil {
		t.Fatal("invalid tenant accepted")
	}
}
