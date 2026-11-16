package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/tenant"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// powerDriver is a vmm.Driver that counts boots. Shutdown waits for
// release when it is set, so a test can act while a stop runs.
type powerDriver struct {
	mu      sync.Mutex
	state   vmm.State
	boots   int
	resumes int
	release chan struct{}
	entered chan struct{}
}

func (d *powerDriver) set(s vmm.State) { d.mu.Lock(); d.state = s; d.mu.Unlock() }
func (d *powerDriver) Name() string    { return "fake" }
func (d *powerDriver) Create(context.Context, string, vmm.Spec) error {
	return nil
}
func (d *powerDriver) Boot(context.Context, string) error {
	d.mu.Lock()
	d.boots++
	d.state = vmm.StateRunning
	d.mu.Unlock()
	return nil
}
func (d *powerDriver) Pause(context.Context, string) error { d.set(vmm.StatePaused); return nil }
func (d *powerDriver) Resume(context.Context, string) error {
	d.mu.Lock()
	d.resumes++
	d.state = vmm.StateRunning
	d.mu.Unlock()
	return nil
}
func (d *powerDriver) Shutdown(context.Context, string, bool) error {
	if d.entered != nil {
		close(d.entered)
	}
	if d.release != nil {
		<-d.release
	}
	d.set(vmm.StateStopped)
	return nil
}
func (d *powerDriver) State(context.Context, string) (vmm.State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state == "" {
		return vmm.StateStopped, nil
	}
	return d.state, nil
}
func (d *powerDriver) DialGuest(context.Context, string, uint32) (net.Conn, error) {
	return nil, errors.New("no guest")
}
func (d *powerDriver) ListenGuestPort(context.Context, string, uint32) (net.Listener, error) {
	return nil, errors.New("no guest")
}
func (d *powerDriver) counts() (boots, resumes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.boots, d.resumes
}

// setupController serves the relay with a real vmm.Controller as Power.
func setupController(t *testing.T, d *powerDriver) (*httptest.Server, *vmm.Controller) {
	srv, ctl, _ := setupControllerGuest(t, d)
	return srv, ctl
}

func setupControllerGuest(t *testing.T, d *powerDriver) (*httptest.Server, *vmm.Controller, *fakeGuest) {
	t.Helper()
	ctl, err := vmm.NewController(vmm.Options{Driver: d, Logger: slog.New(slog.DiscardHandler),
		Spec:  func(context.Context, string) (vmm.Spec, error) { return vmm.Spec{}, nil },
		Ready: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGuest{events: make(chan json.RawMessage, 16), mode: rpc.ModeObserver}
	rl, err := New(Options{Guest: g, Power: ctl, Logger: slog.New(slog.DiscardHandler),
		Running: func(tid string) bool { return ctl.State(tid) == vmm.StateRunning }, Stopped: ctl.Held})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/desktop/ws", rl.ServeWS)
	mux.HandleFunc("GET /api/v1/desktop/mode", rl.ServeMode)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.WithID(r.Context(), "default")))
	}))
	t.Cleanup(srv.Close)
	return srv, ctl, g
}

func TestDesktopSocketDoesNotStartAVMThatTheUserStopped(t *testing.T) {
	d := &powerDriver{}
	srv, ctl := setupController(t, d)
	ctx := context.Background()
	if err := ctl.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Stop(ctx, "default", false); err != nil {
		t.Fatal(err)
	}

	// The page reconnects its stream after the stop.
	c := dial(t, srv)
	m := readMsg(t, c, "error")
	if m["code"] != CodeStopped {
		t.Fatalf("error = %v, want code %s", m, CodeStopped)
	}
	if boots, _ := d.counts(); boots != 1 {
		t.Fatalf("boots = %d after the stopped socket, want 1", boots)
	}
	if st := ctl.State("default"); st != vmm.StateStopped {
		t.Fatalf("state = %s, want STOPPED", st)
	}
	resp, err := http.Get(srv.URL + "/api/v1/desktop/mode")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || ctl.State("default") != vmm.StateStopped {
		t.Fatalf("mode while stopped: status %d state %s", resp.StatusCode, ctl.State("default"))
	}

	// An explicit start lifts the stop; the socket then streams.
	if err := ctl.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	readMsg(t, dial(t, srv), "hello")
	if boots, _ := d.counts(); boots != 2 {
		t.Fatalf("boots = %d, want 2", boots)
	}
}

// A socket that reconnects while the stop runs (the guest stream ended
// first) must not start the VM again when the stop completes.
func TestDesktopSocketDuringStopDoesNotStartTheVM(t *testing.T) {
	d := &powerDriver{release: make(chan struct{}), entered: make(chan struct{})}
	srv, ctl := setupController(t, d)
	ctx := context.Background()
	if err := ctl.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- ctl.Stop(ctx, "default", false) }()
	<-d.entered

	c := dial(t, srv)
	m := readMsg(t, c, "error")
	if m["code"] != CodeStopped {
		t.Fatalf("error = %v, want code %s", m, CodeStopped)
	}
	close(d.release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if boots, _ := d.counts(); boots != 1 || ctl.State("default") != vmm.StateStopped {
		t.Fatalf("boots = %d state = %s, want 1 and STOPPED", boots, ctl.State("default"))
	}
}

// An idle pause is not a user stop: the socket wakes the VM.
func TestDesktopSocketWakesAnIdlePausedVM(t *testing.T) {
	d := &powerDriver{}
	srv, ctl := setupController(t, d)
	ctx := context.Background()
	if err := ctl.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.Hibernate(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	readMsg(t, dial(t, srv), "hello")
	if _, resumes := d.counts(); resumes != 1 {
		t.Fatalf("resumes = %d, want 1", resumes)
	}
}

// A stream that ends because the user stops the VM ends with the stopped
// code, which the browser does not retry.
func TestDesktopStreamEndedByAUserStopSaysStopped(t *testing.T) {
	d := &powerDriver{release: make(chan struct{}), entered: make(chan struct{})}
	srv, ctl, g := setupControllerGuest(t, d)
	ctx := context.Background()
	if err := ctl.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	c := dial(t, srv)
	readMsg(t, c, "hello")
	stopped := make(chan error, 1)
	go func() { stopped <- ctl.Stop(ctx, "default", false) }()
	<-d.entered
	g.mu.Lock()
	sig := g.signals[0]
	g.mu.Unlock()
	sig.Close() // the guest goes down
	if m := readMsg(t, c, "error"); m["code"] != CodeStopped {
		t.Fatalf("error %v, want code %s", m, CodeStopped)
	}
	close(d.release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}
