//go:build unix

// Package agentfstest starts an in-process guest daemon over a UNIX socket
// that serves two temporary roots, for tests that exercise the provider
// storage mode (remotefs, GuestDiscovery) against the real RPC methods.
package agentfstest

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/agentfs/remotefs"
	"github.com/portwatcher/lydiabox/internal/guest/guestfs"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/guest/rpc/daemon"
	"github.com/portwatcher/lydiabox/internal/vmm/guestclient"
)

// Guest is a running in-process guest daemon and its host clients.
type Guest struct {
	Home, Workspace string
	Client          *guestclient.Client
	FS              *remotefs.FS
	Waker           *Waker
}

type unixDialer struct{ path string }

func (d unixDialer) DialGuest(ctx context.Context, _ string, port uint32) (net.Conn, error) {
	if port != rpc.PortGuestRPC {
		return nil, io.ErrUnexpectedEOF
	}
	return rpc.Dial(ctx, "unix://"+d.path)
}

// Start serves home and workspace (created when missing) through a guest
// daemon on a UNIX socket and returns a remotefs.FS whose waker and leaser
// is a Waker.
func Start(t testing.TB, home, workspace string) *Guest {
	t.Helper()
	for _, d := range []string{home, workspace} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A short directory: UNIX socket paths are limited to about 100 bytes.
	dir, err := os.MkdirTemp("", "lbxfs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "guest.sock")
	ln, err := rpc.Listen("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	files, err := guestfs.New(guestfs.Options{Home: home, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	srv := rpc.NewServer(log)
	daemon.Register(srv, daemon.Options{Files: files})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	client := guestclient.New(guestclient.Options{Dialer: unixDialer{sock}, Logger: log})
	w := &Waker{leases: map[string]int{}}
	fsys, err := remotefs.New(remotefs.Options{Client: client, Waker: w, Leaser: w, WakeTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return &Guest{Home: home, Workspace: workspace, Client: client, FS: fsys, Waker: w}
}

// Waker is a fake VM controller: it counts wakes and leases and can delay
// or fail EnsureRunning.
type Waker struct {
	mu     sync.Mutex
	wakes  int
	leases map[string]int
	// Delay blocks EnsureRunning (until ctx ends when it is longer).
	Delay time.Duration
	// Err fails EnsureRunning.
	Err error
}

// EnsureRunning implements remotefs.Waker.
func (w *Waker) EnsureRunning(ctx context.Context, _ string) error {
	w.mu.Lock()
	w.wakes++
	delay, err := w.Delay, w.Err
	w.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// AcquireLease implements remotefs.Leaser.
func (w *Waker) AcquireLease(_, kind string) func() {
	w.mu.Lock()
	w.leases[kind]++
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			w.leases[kind]--
			w.mu.Unlock()
		})
	}
}

// Set changes the delay and the error of EnsureRunning.
func (w *Waker) Set(delay time.Duration, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Delay, w.Err = delay, err
}

// Wakes returns the number of EnsureRunning calls.
func (w *Waker) Wakes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wakes
}

// Leases returns the active lease count of kind.
func (w *Waker) Leases(kind string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.leases[kind]
}
