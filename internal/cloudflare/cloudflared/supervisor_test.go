package cloudflared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/storage"
)

// The test binary doubles as a fake cloudflared: with FAKE_CLOUDFLARED set,
// TestMain runs fakeMain instead of the tests. Modes:
//
//	ready   serve /ready 200 until SIGTERM
//	crash   exit 3 at once
//	nolink  serve /ready 503 (no edge connection)
const fakeEnv = "FAKE_CLOUDFLARED"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(fakeMain(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeMain(mode string, args []string) int {
	want := []string{"tunnel", "--no-autoupdate", "--config"}
	if len(args) < 7 || !slices.Equal(args[:3], want) || args[4] != "--metrics" || args[6] != "run" {
		fmt.Fprintf(os.Stderr, "unexpected args %q\n", args)
		return 2
	}
	if os.Getenv("TUNNEL_TOKEN") != "tok-123" || slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "tok-123") }) {
		fmt.Fprintln(os.Stderr, "the token must come from TUNNEL_TOKEN only")
		return 2
	}
	if _, err := os.Stat(args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "config file missing:", err)
		return 2
	}
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "ERR Unauthorized: Invalid tunnel secret")
		return 3
	}
	status := http.StatusOK
	if mode == "nolink" {
		status = http.StatusServiceUnavailable
	}
	ln, err := net.Listen("tcp", args[5])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/ready" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(status)
		}))
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	return 0
}

func newSupervisor(t *testing.T, mode string) *Supervisor {
	t.Helper()
	t.Setenv(fakeEnv, mode)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSupervisor(Options{
		TenantID: "default", Binary: exe, Token: "tok-123", DataDir: filepath.Join(t.TempDir(), "cloudflared"),
		Runner: storage.OSRunner{}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		HealthInterval: 20 * time.Millisecond, MinBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond,
		StopTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSupervisorConnectsAndStops(t *testing.T) {
	s := newSupervisor(t, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err == nil {
		t.Fatal("a second Start must fail")
	}
	if err := s.WaitConnected(ctx); err != nil {
		t.Fatalf("not connected: %+v", s.Status())
	}
	if st := s.Status(); st.Restarts != 0 || st.StartedAt.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	cfg, err := os.ReadFile(filepath.Join(s.opts.DataDir, "config.yml"))
	if err != nil || !strings.Contains(string(cfg), "no-autoupdate") {
		t.Fatalf("config = %q, %v", cfg, err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.State != StateStopped || st.Restarts != 0 {
		t.Fatalf("after stop: %+v (a clean stop must not count as a restart)", st)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatal("Stop must be idempotent")
	}
}

func TestSupervisorRestartsWithBackoff(t *testing.T) {
	s := newSupervisor(t, "crash")
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "three restarts", func() bool { return s.Status().Restarts >= 3 })
	st := s.Status()
	if st.State != StateError && st.State != StateConnecting {
		t.Fatalf("state = %+v", st)
	}
	if st.LastError == "" {
		t.Fatal("want the exit error")
	}
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorReportsMissingConnection(t *testing.T) {
	s := newSupervisor(t, "nolink")
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if st := s.Status(); st.State != StateConnecting || st.Restarts != 0 {
		t.Fatalf("status = %+v", st)
	}
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorWithoutBinary(t *testing.T) {
	s, err := NewSupervisor(Options{TenantID: "default", Token: "t", DataDir: t.TempDir(), BinaryError: "no cloudflared"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.State != StateError || st.LastError != "no cloudflared" {
		t.Fatalf("status = %+v", st)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNewSupervisorValidates(t *testing.T) {
	for _, o := range []Options{
		{Token: "t", DataDir: "d"},
		{TenantID: "default", Token: "t"},
		{TenantID: "default", DataDir: "d"},
	} {
		if _, err := NewSupervisor(o); err == nil {
			t.Errorf("want error for %+v", o)
		}
	}
}

func TestStopTimesOutWithContext(t *testing.T) {
	s := newSupervisor(t, "ready")
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Stop(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
