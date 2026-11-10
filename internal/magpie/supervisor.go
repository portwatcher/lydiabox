// Package magpie runs the yetone/magpie LLM gateway on the host control
// plane: process supervision with health polling and restart backoff, the
// named gateway key of the VM, header injection for the guest tunnel, and a
// typed client for the magpie admin API used by the models routes.
// Upstream API keys stay in the magpie data directory on the host.
package magpie

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lydiabox/lydiabox/internal/storage"
)

// Defaults of the host magpie process.
const (
	DefaultAddr    = "127.0.0.1:3425"
	DefaultWebAddr = "127.0.0.1:3426"
	// GatewayKeyName names the gateway key that the VM tunnel uses.
	GatewayKeyName = "lydiabox-vm"
)

// Secret names in the store (table magpie_secrets).
const (
	secretGatewayKey   = "gateway-key"
	secretGatewayKeyID = "gateway-key-id"
	secretWebKey       = "web-key"
)

// Gateway states reported by GET /api/v1/models/gateway.
const (
	StateStarting = "starting"
	StateRunning  = "running"
	StateStopped  = "stopped"
	StateError    = "error"
)

// SecretStore persists encrypted magpie secrets (implemented by
// *store.Store). Get returns an error wrapping store.ErrNotFound when the
// secret does not exist.
type SecretStore interface {
	GetMagpieSecret(ctx context.Context, tenantID, name string) (string, error)
	PutMagpieSecret(ctx context.Context, tenantID, name, secret string) error
}

// Options configures a Supervisor.
type Options struct {
	TenantID string
	// Binary is the magpie executable (bootstrap.Resolve("magpie")). When
	// empty, the supervisor stays in StateError with BinaryError.
	Binary      string
	BinaryError string
	// DataDir is the magpie data directory ($DATA_DIR/state/magpie). magpie
	// keeps its config under $XDG_CONFIG_HOME/magpie, so the supervisor sets
	// XDG_CONFIG_HOME and XDG_STATE_HOME to its parent.
	DataDir string
	// CacheDir is $XDG_CACHE_HOME for magpie (default DataDir/../../cache).
	CacheDir string
	Addr     string
	WebAddr  string
	Runner   storage.Runner
	Secrets  SecretStore
	// NewAdmin builds the admin client; the default is NewHTTPAdmin.
	NewAdmin func(addr, webAddr, webKey string) AdminAPI
	Logger   *slog.Logger

	HealthInterval time.Duration // default 5s
	StopTimeout    time.Duration // default 10s
	MinBackoff     time.Duration // default 1s
	MaxBackoff     time.Duration // default 30s
	// StableAfter resets the backoff after a run this long (default 60s).
	StableAfter time.Duration
}

// Status is a snapshot of the gateway state.
type Status struct {
	State     string    `json:"status"`
	Addr      string    `json:"addr"`
	Version   string    `json:"version,omitempty"`
	LastError string    `json:"lastError,omitempty"`
	Restarts  int       `json:"restarts"`
	StartedAt time.Time `json:"startedAt"`
}

// Supervisor runs one magpie process for one tenant.
type Supervisor struct {
	opts Options
	log  *slog.Logger

	mu       sync.Mutex
	status   Status
	admin    AdminAPI
	cancel   context.CancelFunc
	done     chan struct{}
	keyReady bool
	webKey   string

	gatewayKey atomic.Value // string
}

// NewSupervisor validates opts and returns a stopped supervisor.
func NewSupervisor(opts Options) (*Supervisor, error) {
	if opts.TenantID == "" {
		return nil, errors.New("magpie: TenantID is required")
	}
	if opts.DataDir == "" {
		return nil, errors.New("magpie: DataDir is required")
	}
	if opts.Secrets == nil {
		return nil, errors.New("magpie: Secrets is required")
	}
	if opts.Runner == nil {
		opts.Runner = storage.OSRunner{}
	}
	if opts.Addr == "" {
		opts.Addr = DefaultAddr
	}
	if opts.WebAddr == "" {
		opts.WebAddr = DefaultWebAddr
	}
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(filepath.Dir(filepath.Dir(opts.DataDir)), "cache")
	}
	if opts.NewAdmin == nil {
		opts.NewAdmin = func(addr, web, key string) AdminAPI { return NewHTTPAdmin(addr, web, key) }
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
	s := &Supervisor{opts: opts, log: opts.Logger.With("component", "magpie")}
	s.status = Status{State: StateStopped, Addr: opts.Addr}
	s.gatewayKey.Store("")
	return s, nil
}

// Addr is the gateway address on the host.
func (s *Supervisor) Addr() string { return s.opts.Addr }

// Status returns the current gateway state.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Admin returns the admin client, or an error wrapping ErrUnavailable while
// the supervisor is not started.
func (s *Supervisor) Admin() (AdminAPI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.admin == nil {
		return nil, fmt.Errorf("%w: gateway is %s", ErrUnavailable, s.status.State)
	}
	return s.admin, nil
}

// GatewayKey returns the provisioned gateway key ("" until provisioned).
func (s *Supervisor) GatewayKey() string { return s.gatewayKey.Load().(string) }

// InjectHeaders replaces the credential of a request from the guest with
// the VM gateway key. The guest only knows a placeholder token, so a
// compromised VM never holds a reusable magpie credential. Phase 6 installs
// this as the InjectHeaders hook of the host side of the LLM tunnel.
func (s *Supervisor) InjectHeaders(h http.Header) {
	key := s.GatewayKey()
	if key == "" {
		return
	}
	// The tunnel strips guest credentials first, so set both forms magpie
	// accepts: OpenAI/Anthropic bearer and the Anthropic x-api-key.
	h.Set("Authorization", "Bearer "+key)
	h.Set("X-Api-Key", key)
	if h.Get("X-Goog-Api-Key") != "" {
		h.Set("X-Goog-Api-Key", key)
	}
}

// Start launches magpie in the background. It returns after preparing the
// data directory and secrets; process failures are reported in Status and
// retried with backoff.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("magpie: already started")
	}
	s.mu.Unlock()
	if s.opts.Binary == "" {
		msg := s.opts.BinaryError
		if msg == "" {
			msg = "magpie binary not installed (run `lydiabox runtime install`)"
		}
		s.setState(StateError, msg)
		s.log.Warn("magpie gateway disabled", "reason", msg)
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.opts.DataDir, "home"), 0o700); err != nil {
		return fmt.Errorf("magpie: create data dir: %w", err)
	}
	if err := os.MkdirAll(s.opts.CacheDir, 0o700); err != nil {
		return fmt.Errorf("magpie: create cache dir: %w", err)
	}
	webKey, err := s.secret(ctx, secretWebKey, func() (string, error) { return randomToken("", 24) })
	if err != nil {
		return err
	}
	if key, err := s.opts.Secrets.GetMagpieSecret(ctx, s.opts.TenantID, secretGatewayKey); err == nil {
		// A previously provisioned key works before magpie answers, so a
		// tunnel request during a restart still carries it.
		s.gatewayKey.Store(key)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.admin = s.opts.NewAdmin(s.opts.Addr, s.opts.WebAddr, webKey)
	s.webKey = webKey
	s.cancel = cancel
	s.done = make(chan struct{})
	s.status.State = StateStarting
	s.mu.Unlock()
	go s.run(runCtx)
	return nil
}

// Stop terminates magpie (SIGTERM, then SIGKILL after StopTimeout).
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

// WaitReady blocks until magpie answers and the gateway key is provisioned.
func (s *Supervisor) WaitReady(ctx context.Context) error {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		s.mu.Lock()
		ok := s.status.State == StateRunning && s.keyReady
		s.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Supervisor) setState(state, lastError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = state
	if lastError != "" || state == StateRunning {
		s.status.LastError = lastError
	}
}

func (s *Supervisor) command() storage.Command {
	parent := filepath.Dir(s.opts.DataDir)
	return storage.Command{
		Path: s.opts.Binary,
		Args: []string{"serve", "--web=" + s.opts.WebAddr},
		Env: []string{
			"MAGPIE_ADDR=" + s.opts.Addr,
			"MAGPIE_WEB_KEY=" + s.webKey,
			"XDG_CONFIG_HOME=" + parent,
			"XDG_STATE_HOME=" + parent,
			"XDG_CACHE_HOME=" + s.opts.CacheDir,
			// A private HOME keeps magpie from detecting and rewiring the
			// host user's own agent configs.
			"HOME=" + filepath.Join(s.opts.DataDir, "home"),
		},
		Dir:    s.opts.DataDir,
		Stdout: logWriter{s.log, slog.LevelDebug},
		Stderr: logWriter{s.log, slog.LevelInfo},
	}
}

// run is the supervision loop: start, poll health, restart with backoff.
func (s *Supervisor) run(ctx context.Context) {
	defer close(s.done)
	backoff := s.opts.MinBackoff
	for ctx.Err() == nil {
		started := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		msg := "magpie exited"
		if err != nil {
			msg = err.Error()
		}
		s.mu.Lock()
		s.status.State = StateError
		s.status.LastError = msg
		s.status.Restarts++
		s.keyReady = false
		s.mu.Unlock()
		if time.Since(started) >= s.opts.StableAfter {
			backoff = s.opts.MinBackoff
		}
		s.log.Warn("magpie stopped; restarting", "err", msg, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.opts.MaxBackoff)
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	proc, err := s.opts.Runner.Start(s.command())
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.status.State = StateStarting
	s.status.StartedAt = time.Now().UTC()
	s.mu.Unlock()
	s.log.Info("magpie started", "pid", proc.Pid(), "addr", s.opts.Addr)
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()

	t := time.NewTicker(s.opts.HealthInterval)
	defer t.Stop()
	s.checkHealth(ctx)
	for {
		select {
		case err := <-exited:
			if err == nil {
				return errors.New("magpie exited")
			}
			return fmt.Errorf("magpie exited: %w", err)
		case <-ctx.Done():
			s.terminate(proc, exited)
			return nil
		case <-t.C:
			s.checkHealth(ctx)
		}
	}
}

func (s *Supervisor) terminate(proc storage.Process, exited <-chan error) {
	_ = proc.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(s.opts.StopTimeout):
		s.log.Warn("magpie did not stop; killing")
		_ = proc.Kill()
		<-exited
	}
}

func (s *Supervisor) checkHealth(ctx context.Context) {
	admin, err := s.Admin()
	if err != nil {
		return
	}
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	h, err := admin.Hello(hctx)
	if err != nil {
		s.mu.Lock()
		if s.status.State == StateRunning {
			s.status.State = StateError
			s.status.LastError = err.Error()
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.status.State = StateRunning
	s.status.Version = h.Version
	s.status.LastError = ""
	ready := s.keyReady
	s.mu.Unlock()
	if !ready {
		if err := s.provisionKey(hctx, admin); err != nil {
			s.log.Warn("magpie gateway key provisioning failed; retrying", "err", err)
			return
		}
		s.mu.Lock()
		s.keyReady = true
		s.mu.Unlock()
	}
}

// secret loads a stored secret or creates and stores it with gen.
func (s *Supervisor) secret(ctx context.Context, name string, gen func() (string, error)) (string, error) {
	v, err := s.opts.Secrets.GetMagpieSecret(ctx, s.opts.TenantID, name)
	if err == nil {
		return v, nil
	}
	if !isNotFound(err) {
		return "", fmt.Errorf("magpie: load %s: %w", name, err)
	}
	if v, err = gen(); err != nil {
		return "", err
	}
	if err := s.opts.Secrets.PutMagpieSecret(ctx, s.opts.TenantID, name, v); err != nil {
		return "", fmt.Errorf("magpie: save %s: %w", name, err)
	}
	return v, nil
}

func randomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

type logWriter struct {
	log   *slog.Logger
	level slog.Level
}

func (w logWriter) Write(p []byte) (int, error) {
	w.log.Log(context.Background(), w.level, "magpie output", "line", string(trimNewline(p)))
	return len(p), nil
}

func trimNewline(p []byte) []byte {
	for len(p) > 0 && (p[len(p)-1] == '\n' || p[len(p)-1] == '\r') {
		p = p[:len(p)-1]
	}
	return p
}
