// Package cloudflared runs the Cloudflare Tunnel connector (cloudflared) as
// a supervised child process of `lydiabox up`: start, readiness polling on
// its metrics endpoint, restart with backoff, and SIGTERM on stop. The
// tunnel token reaches the child through the TUNNEL_TOKEN environment
// variable, never through argv.
package cloudflared

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lydiabox/lydiabox/internal/storage"
)

// Tunnel states reported by Status.
const (
	StateStopped    = "stopped"
	StateConnecting = "connecting"
	StateConnected  = "connected"
	StateError      = "error"
)

// Options configures a Supervisor.
type Options struct {
	TenantID string
	// Binary is the cloudflared executable. When empty, Start reports
	// StateError with BinaryError and runs nothing.
	Binary      string
	BinaryError string
	// Token is the tunnel token from `lydiabox cloudflare setup`.
	Token string
	// DataDir holds the private config file and HOME of cloudflared.
	DataDir string
	// MetricsAddr is the readiness endpoint address; empty picks a free
	// 127.0.0.1 port on every launch.
	MetricsAddr string
	Runner      storage.Runner
	HTTPClient  *http.Client
	Logger      *slog.Logger

	HealthInterval time.Duration // default 5s
	StopTimeout    time.Duration // default 10s
	MinBackoff     time.Duration // default 1s
	MaxBackoff     time.Duration // default 30s
	StableAfter    time.Duration // default 60s: resets the backoff
}

// Status is a snapshot of the connector state.
type Status struct {
	State     string    `json:"state"`
	LastError string    `json:"lastError,omitempty"`
	Restarts  int       `json:"restarts"`
	StartedAt time.Time `json:"startedAt"`
}

// Supervisor runs one cloudflared process.
type Supervisor struct {
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	status Status
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSupervisor validates opts and returns a stopped supervisor.
func NewSupervisor(opts Options) (*Supervisor, error) {
	if opts.TenantID == "" {
		return nil, errors.New("cloudflared: TenantID is required")
	}
	if opts.DataDir == "" {
		return nil, errors.New("cloudflared: DataDir is required")
	}
	if opts.Token == "" {
		return nil, errors.New("cloudflared: tunnel token is required")
	}
	if opts.Runner == nil {
		opts.Runner = storage.OSRunner{}
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 3 * time.Second}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&opts.HealthInterval, 5*time.Second)
	def(&opts.StopTimeout, 10*time.Second)
	def(&opts.MinBackoff, time.Second)
	def(&opts.MaxBackoff, 30*time.Second)
	def(&opts.StableAfter, time.Minute)
	return &Supervisor{
		opts:   opts,
		log:    opts.Logger.With("component", "cloudflared", "tenant", opts.TenantID),
		status: Status{State: StateStopped},
	}, nil
}

// Status returns the connector state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Start launches cloudflared in the background. Process failures are
// reported in Status and retried with backoff.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return errors.New("cloudflared: already started")
	}
	if s.opts.Binary == "" {
		msg := s.opts.BinaryError
		if msg == "" {
			msg = "cloudflared is not installed (run `lydiabox runtime install-local cloudflared <file>` or put it on PATH)"
		}
		s.status = Status{State: StateError, LastError: msg}
		s.log.Warn("Cloudflare Tunnel disabled", "reason", msg)
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.opts.DataDir, "home"), 0o700); err != nil {
		return fmt.Errorf("cloudflared: create data dir: %w", err)
	}
	// A private config file keeps cloudflared from reading a host
	// ~/.cloudflared/config.yml; the ingress rules come from Cloudflare.
	if err := os.WriteFile(s.configPath(), []byte("no-autoupdate: true\n"), 0o600); err != nil {
		return fmt.Errorf("cloudflared: write config: %w", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	s.status.State = StateConnecting
	go s.run(runCtx)
	return nil
}

// Stop terminates cloudflared (SIGTERM, then SIGKILL after StopTimeout).
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.cancel = nil
	s.status.State = StateStopped
	s.mu.Unlock()
	return nil
}

// WaitConnected blocks until the tunnel has a connection to Cloudflare.
func (s *Supervisor) WaitConnected(ctx context.Context) error {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		if s.Status().State == StateConnected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Supervisor) configPath() string { return filepath.Join(s.opts.DataDir, "config.yml") }

func (s *Supervisor) command(metrics string) storage.Command {
	return storage.Command{
		Path: s.opts.Binary,
		Args: []string{"tunnel", "--no-autoupdate", "--config", s.configPath(), "--metrics", metrics, "run"},
		Env: []string{
			"TUNNEL_TOKEN=" + s.opts.Token,
			"HOME=" + filepath.Join(s.opts.DataDir, "home"),
		},
		Dir:    s.opts.DataDir,
		Stdout: logWriter{s.log, slog.LevelDebug},
		Stderr: logWriter{s.log, slog.LevelInfo},
	}
}

func (s *Supervisor) run(ctx context.Context) {
	defer close(s.done)
	backoff := s.opts.MinBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		msg := "cloudflared exited"
		if err != nil {
			msg = err.Error()
		}
		s.mu.Lock()
		s.status.State = StateError
		s.status.LastError = msg
		s.status.Restarts++
		s.mu.Unlock()
		if time.Since(started) >= s.opts.StableAfter {
			backoff = s.opts.MinBackoff
		}
		s.log.Warn("cloudflared stopped; restarting", "err", msg, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.opts.MaxBackoff)
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	metrics := s.opts.MetricsAddr
	if metrics == "" {
		port, err := freePort()
		if err != nil {
			return err
		}
		metrics = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	proc, err := s.opts.Runner.Start(s.command(metrics))
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.status.State = StateConnecting
	s.status.StartedAt = time.Now().UTC()
	s.mu.Unlock()
	s.log.Info("cloudflared started", "pid", proc.Pid(), "metrics", metrics)
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()

	t := time.NewTicker(s.opts.HealthInterval)
	defer t.Stop()
	for {
		select {
		case err := <-exited:
			if err == nil {
				return errors.New("cloudflared exited")
			}
			return fmt.Errorf("cloudflared exited: %w", err)
		case <-ctx.Done():
			s.terminate(proc, exited)
			return nil
		case <-t.C:
			s.checkReady(ctx, metrics)
		}
	}
}

// checkReady polls /ready, which answers 200 once cloudflared holds at
// least one connection to the Cloudflare edge.
func (s *Supervisor) checkReady(ctx context.Context, metrics string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+metrics+"/ready", nil)
	if err != nil {
		return
	}
	resp, err := s.opts.HTTPClient.Do(req)
	state, lastErr := StateConnected, ""
	if err != nil {
		state, lastErr = StateConnecting, "metrics endpoint not reachable yet"
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			state, lastErr = StateConnecting, fmt.Sprintf("no connection to Cloudflare yet (ready: HTTP %d)", resp.StatusCode)
		}
	}
	s.mu.Lock()
	prev := s.status.State
	s.status.State = state
	if state == StateConnected || prev == StateConnected {
		s.status.LastError = lastErr
	}
	s.mu.Unlock()
	if state != prev && state == StateConnected {
		s.log.Info("Cloudflare Tunnel connected")
	} else if state != prev && prev == StateConnected {
		s.log.Warn("Cloudflare Tunnel lost its connection", "reason", lastErr)
	}
}

func (s *Supervisor) terminate(proc storage.Process, exited <-chan error) {
	_ = proc.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(s.opts.StopTimeout):
		s.log.Warn("cloudflared did not stop; killing")
		_ = proc.Kill()
		<-exited
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

type logWriter struct {
	log   *slog.Logger
	level slog.Level
}

func (w logWriter) Write(p []byte) (int, error) {
	line := p
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	w.log.Log(context.Background(), w.level, "cloudflared output", "line", string(line))
	return len(p), nil
}
