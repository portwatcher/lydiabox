package magpie_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/magpie"
	"github.com/portwatcher/lydiabox/internal/magpie/magpietest"
	"github.com/portwatcher/lydiabox/internal/storage"
	"github.com/portwatcher/lydiabox/internal/store"
)

// fakeProc is a process that runs until killed, signalled, or crashed.
type fakeProc struct {
	once sync.Once
	done chan struct{}
	err  error
	sigs []os.Signal
	mu   sync.Mutex
}

func newProc() *fakeProc { return &fakeProc{done: make(chan struct{})} }

func (p *fakeProc) exit(err error) {
	p.once.Do(func() { p.err = err; close(p.done) })
}
func (p *fakeProc) Pid() int { return 4242 }
func (p *fakeProc) Signal(sig os.Signal) error {
	p.mu.Lock()
	p.sigs = append(p.sigs, sig)
	p.mu.Unlock()
	p.exit(nil)
	return nil
}
func (p *fakeProc) Kill() error { p.exit(errors.New("killed")); return nil }
func (p *fakeProc) Wait() error { <-p.done; return p.err }

type fakeRunner struct {
	mu    sync.Mutex
	cmds  []storage.Command
	procs []*fakeProc
}

func (r *fakeRunner) Run(context.Context, storage.Command) error { return nil }
func (r *fakeRunner) Start(c storage.Command) (storage.Process, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := newProc()
	r.cmds = append(r.cmds, c)
	r.procs = append(r.procs, p)
	return p, nil
}
func (r *fakeRunner) last() (*fakeProc, storage.Command, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.procs[len(r.procs)-1], r.cmds[len(r.cmds)-1], len(r.procs)
}

// memSecrets is an in-memory SecretStore.
type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSecrets) GetMagpieSecret(_ context.Context, tenantID, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[tenantID+"/"+name]
	if !ok {
		return "", store.ErrNotFound
	}
	return v, nil
}
func (s *memSecrets) PutMagpieSecret(_ context.Context, tenantID, name, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[tenantID+"/"+name] = secret
	return nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newSupervisor(t *testing.T, fake *magpietest.FakeAdmin, secrets *memSecrets) (*magpie.Supervisor, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{}
	data := filepath.Join(t.TempDir(), "state", "magpie")
	sup, err := magpie.NewSupervisor(magpie.Options{
		TenantID: "default", Binary: "/opt/magpie", DataDir: data, Runner: runner, Secrets: secrets,
		NewAdmin:       func(string, string, string) magpie.AdminAPI { return fake },
		Logger:         quietLogger(),
		HealthInterval: 10 * time.Millisecond, MinBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond,
		StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sup, runner
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSupervisorLifecycleAndGatewayKey(t *testing.T) {
	fake := magpietest.NewFakeAdmin()
	secrets := &memSecrets{m: map[string]string{}}
	sup, runner := newSupervisor(t, fake, secrets)
	ctx := context.Background()
	if err := sup.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sup.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	st := sup.Status()
	if st.State != magpie.StateRunning || st.Version != "0.1.3531" || st.Addr != "127.0.0.1:3425" {
		t.Fatalf("status = %+v", st)
	}
	_, cmd, _ := runner.last()
	if cmd.Path != "/opt/magpie" || strings.Join(cmd.Args, " ") != "serve --web=127.0.0.1:3426" {
		t.Fatalf("command = %s %v", cmd.Path, cmd.Args)
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"MAGPIE_ADDR=127.0.0.1:3425", "MAGPIE_WEB_KEY=", "XDG_CONFIG_HOME=", "HOME="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s:\n%s", want, env)
		}
	}
	key := sup.GatewayKey()
	if !strings.HasPrefix(key, "lbx-gw-") || fake.SecretOf(magpie.GatewayKeyName) != key {
		t.Fatalf("gateway key %q not registered (magpie has %q)", key, fake.SecretOf(magpie.GatewayKeyName))
	}
	stored, _ := secrets.GetMagpieSecret(ctx, "default", "gateway-key")
	if stored != key {
		t.Fatal("gateway key not persisted")
	}

	h := http.Header{}
	h.Set("Authorization", "Bearer lydiabox-local")
	h.Set("X-Api-Key", "lydiabox-local")
	sup.InjectHeaders(h)
	if h.Get("Authorization") != "Bearer "+key || h.Get("X-Api-Key") != key || h.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("InjectHeaders = %v", h)
	}

	// A crash restarts magpie with backoff and keeps the same key.
	proc, _, n := runner.last()
	proc.exit(errors.New("segfault"))
	waitFor(t, "restart", func() bool { _, _, m := runner.last(); return m > n })
	if err := sup.WaitReady(wctx); err != nil {
		t.Fatal(err)
	}
	if sup.Status().Restarts != 1 || sup.GatewayKey() != key {
		t.Fatalf("after restart: %+v key changed=%v", sup.Status(), sup.GatewayKey() != key)
	}
	if got := len(fake.GatewayKeys); got != 1 {
		t.Fatalf("restart registered %d keys, want 1", got)
	}

	proc, _, _ = runner.last()
	if err := sup.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if sup.Status().State != magpie.StateStopped {
		t.Fatalf("state after stop = %s", sup.Status().State)
	}
	proc.mu.Lock()
	sigs := len(proc.sigs)
	proc.mu.Unlock()
	if sigs != 1 {
		t.Fatalf("magpie got %d signals, want SIGTERM", sigs)
	}
}

func TestGatewayKeyReplacesForeignKey(t *testing.T) {
	fake := magpietest.NewFakeAdmin()
	// A key with our name that this database never registered.
	if _, err := fake.AddGatewayKey(context.Background(), magpie.GatewayKeyName, "someone-elses"); err != nil {
		t.Fatal(err)
	}
	secrets := &memSecrets{m: map[string]string{}}
	sup, _ := newSupervisor(t, fake, secrets)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sup.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.GatewayKeys) != 1 || fake.SecretOf(magpie.GatewayKeyName) != sup.GatewayKey() {
		t.Fatalf("foreign key not replaced: %+v", fake.GatewayKeys)
	}
	if err := sup.RotateGatewayKey(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.GatewayKeys) != 1 || fake.SecretOf(magpie.GatewayKeyName) != sup.GatewayKey() {
		t.Fatalf("rotation left %+v", fake.GatewayKeys)
	}
}

func TestSupervisorWithoutBinary(t *testing.T) {
	sup, err := magpie.NewSupervisor(magpie.Options{TenantID: "default", DataDir: t.TempDir(),
		Secrets: &memSecrets{m: map[string]string{}}, BinaryError: "magpie: not installed", Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := sup.Status(); st.State != magpie.StateError || st.LastError != "magpie: not installed" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := sup.Admin(); !errors.Is(err, magpie.ErrUnavailable) {
		t.Fatalf("Admin() = %v", err)
	}
	h := http.Header{"Authorization": {"Bearer x"}}
	sup.InjectHeaders(h)
	if h.Get("Authorization") != "Bearer x" {
		t.Fatal("headers changed without a key")
	}
}
