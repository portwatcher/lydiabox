//go:build unix

package instabox_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest"
	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest/fakeguest"
	"github.com/lydiabox/lydiabox/internal/store"
	"github.com/lydiabox/lydiabox/internal/vmm"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

const tenant = "alice"

// env is a fake InstaBox server whose machines run the real guest daemon,
// and a state database shared by the drivers of one test (a restart of
// the host process is a second driver on the same store).
type env struct {
	t        *testing.T
	fake     *instaboxtest.Server
	runner   *fakeguest.Runner
	store    *store.Store
	snapshot string
}

// checkLeaks fails the test when goroutines started during the test are
// still running after every cleanup. Call it first.
func checkLeaks(t *testing.T) {
	t.Helper()
	base := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for runtime.NumGoroutine() > base {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d running, %d before the test\n%s", runtime.NumGoroutine(), base, buf[:n])
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root, err := os.MkdirTemp("", "lbxi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	e := &env{t: t, runner: fakeguest.New(filepath.Join(root, "m"), nil)}
	e.fake = instaboxtest.New(instaboxtest.Options{OpDelay: 10 * time.Millisecond, Guest: e.runner, Exec: e.exec})
	t.Cleanup(func() {
		e.fake.Close()
		e.runner.Close()
	})
	e.snapshot = e.fake.AddSnapshot("lydiabox-desktop")
	st, err := store.Open(context.Background(), filepath.Join(root, "state.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e.store = st
	return e
}

// exec answers the guest restart of the image (supervisorctl) by
// restarting the daemon, and other commands like the default fake.
func (e *env) exec(id string, req instaboxtest.ExecRequest) instaboxtest.ExecResult {
	if strings.Contains(req.Cmd, "supervisorctl") && strings.Contains(req.Cmd, "restart lydiabox-guest") {
		e.runner.Restart(id)
		return instaboxtest.ExecResult{Stdout: "lydiabox-guest: started\n"}
	}
	return instaboxtest.DefaultExec(id, req)
}

func (e *env) client(t *testing.T, retries int) *ib.Client {
	t.Helper()
	return e.clientWith(t, retries, nil)
}

// lostAnswer is a transport that sends a request and then loses the
// answer of the first match: the server acted, the client saw nothing.
type lostAnswer struct {
	mu     sync.Mutex
	method string
	path   string
	left   int
}

func (l *lostAnswer) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil && l.left > 0 && r.Method == l.method && r.URL.Path == l.path {
		l.left--
		resp.Body.Close()
		return nil, errors.New("connection reset (test)")
	}
	return resp, err
}

func (e *env) clientWith(t *testing.T, retries int, rt http.RoundTripper) *ib.Client {
	t.Helper()
	var hc *http.Client
	if rt != nil {
		hc = &http.Client{Transport: rt}
	}
	c, err := ib.NewClient(ib.Options{BaseURL: e.fake.URL, APIKey: e.fake.APIKey(), MaxRetries: retries, HTTPClient: hc,
		RetryBaseDelay: time.Millisecond, MaxRetryDelay: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// driver returns a new driver on the shared store; it is closed at the end
// of the test.
func (e *env) driver(t *testing.T, mutate ...func(*vmmib.Options)) *vmmib.Driver {
	t.Helper()
	opts := vmmib.Options{Client: e.client(t, 0), Store: e.store, Snapshot: e.snapshot, IdlePause: 30 * time.Minute,
		LinkMinBackoff: 50 * time.Millisecond, LinkMaxBackoff: 200 * time.Millisecond, LinkKeepAlive: time.Second,
		LinkWakeTimeout: 10 * time.Second, PollInterval: time.Hour, BlockTime: time.Minute,
		Logger: slog.New(slog.DiscardHandler)}
	for _, m := range mutate {
		m(&opts)
	}
	d, err := vmmib.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// count returns the requests whose method and path match (path prefix).
func (e *env) count(method, prefix string) int {
	n := 0
	for _, r := range e.fake.Requests() {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

func (e *env) machineID(t *testing.T) string {
	t.Helper()
	rec, ok, err := vmmib.LoadRecord(context.Background(), e.store, tenant)
	if err != nil || !ok || rec.MachineID == "" {
		t.Fatalf("record %+v, %v, %v", rec, ok, err)
	}
	return rec.MachineID
}

// boot creates and boots the machine of the tenant.
func boot(t *testing.T, d *vmmib.Driver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Create(ctx, tenant, vmm.Spec{CPUs: 2, MemMB: 4096}); err != nil {
		t.Fatal(err)
	}
	if err := d.Boot(ctx, tenant); err != nil {
		t.Fatal(err)
	}
}

// ping calls the guest daemon over the link until it answers.
func ping(t *testing.T, d *vmmib.Driver) {
	t.Helper()
	gc := guestclient.New(guestclient.Options{Dialer: d, Logger: slog.New(slog.DiscardHandler)})
	defer gc.Reset(tenant)
	deadline := time.Now().Add(20 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := gc.Ping(ctx, tenant)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("guest ping: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func state(t *testing.T, d *vmmib.Driver) vmm.State {
	t.Helper()
	st, err := d.State(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// events records the reports of a state watcher.
type events struct {
	mu  sync.Mutex
	got []vmm.State
}

func (ev *events) add(_ string, st vmm.State) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.got = append(ev.got, st)
}

func (ev *events) wait(t *testing.T, st vmm.State) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		ev.mu.Lock()
		for _, s := range ev.got {
			if s == st {
				ev.mu.Unlock()
				return
			}
		}
		got := append([]vmm.State(nil), ev.got...)
		ev.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("watcher reported %v, want %s", got, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
