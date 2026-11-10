// Package harnesstest provides fakes of the guest PTY runner, the VM waker,
// and the session store for tests of the harness manager, the bridge, and
// the session routes.
package harnesstest

import (
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/store"
)

// PTY is a fake guest terminal. Output written with Emit is read by the
// room; input written by clients is recorded.
type PTY struct {
	id     string
	Spec   harness.PTYSpec
	replay []byte

	r *io.PipeReader
	w *io.PipeWriter

	mu      sync.Mutex
	input   []byte
	signals []string
	sizes   [][2]uint16
	closed  bool
	code    int
	done    chan struct{}
	// IgnoreSIGINT makes the process survive SIGINT (it still exits on
	// SIGTERM), to exercise the escalation path.
	IgnoreSIGINT bool
	// IgnoreSIGTERM makes the process survive SIGTERM too (it still exits
	// on SIGHUP), like an interactive shell.
	IgnoreSIGTERM bool
	// Echo writes input back as output.
	Echo bool
}

func newPTY(spec harness.PTYSpec, replay []byte) *PTY {
	r, w := io.Pipe()
	return &PTY{id: spec.ID, Spec: spec, replay: replay, r: r, w: w, done: make(chan struct{})}
}

// ID implements harness.PTY.
func (p *PTY) ID() string { return p.id }

// Replay implements harness.PTY.
func (p *PTY) Replay() []byte { return p.replay }

// Read implements harness.PTY.
func (p *PTY) Read(b []byte) (int, error) { return p.r.Read(b) }

// Write implements harness.PTY.
func (p *PTY) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.input = append(p.input, b...)
	echo := p.Echo
	p.mu.Unlock()
	if echo {
		go p.Emit(append([]byte(nil), b...))
	}
	return len(b), nil
}

// Resize implements harness.PTY.
func (p *PTY) Resize(cols, rows uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sizes = append(p.sizes, [2]uint16{cols, rows})
	return nil
}

// Signal implements harness.PTY.
func (p *PTY) Signal(sig string) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	ignore := (p.IgnoreSIGINT && sig == "SIGINT") || (p.IgnoreSIGTERM && sig == "SIGTERM")
	p.mu.Unlock()
	if !ignore && (sig == "SIGINT" || sig == "SIGTERM" || sig == "SIGHUP" || sig == "SIGQUIT") {
		code := 130
		if sig == "SIGTERM" {
			code = 143
		}
		p.Exit(code)
	}
	return nil
}

// Wait implements harness.PTY.
func (p *PTY) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.code, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// Close implements harness.PTY (detach only).
func (p *PTY) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

// Emit writes terminal output.
func (p *PTY) Emit(b []byte) { _, _ = p.w.Write(b) }

// Exit ends the process with code.
func (p *PTY) Exit(code int) {
	p.mu.Lock()
	select {
	case <-p.done:
		p.mu.Unlock()
		return
	default:
	}
	p.code = code
	close(p.done)
	p.mu.Unlock()
	_ = p.w.Close()
}

// Input returns everything written to the terminal.
func (p *PTY) Input() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.input...)
}

// Signals returns the signals received.
func (p *PTY) Signals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.signals)
}

// Sizes returns the resize calls.
func (p *PTY) Sizes() [][2]uint16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sizes)
}

// Exited reports whether the process ended.
func (p *PTY) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Runner is a fake harness.PTYRunner.
type Runner struct {
	mu   sync.Mutex
	ptys []*PTY
	// Err fails every StartPTY.
	Err error
	// Replay is the ring buffer content of new PTYs.
	Replay []byte
	// IgnoreSIGINT and IgnoreSIGTERM are copied to new PTYs.
	IgnoreSIGINT  bool
	IgnoreSIGTERM bool
	Echo          bool
	// Started receives every new PTY when non-nil.
	Started chan *PTY
}

// StartPTY implements harness.PTYRunner.
func (r *Runner) StartPTY(_ context.Context, _ string, spec harness.PTYSpec) (harness.PTY, error) {
	r.mu.Lock()
	if r.Err != nil {
		r.mu.Unlock()
		return nil, r.Err
	}
	p := newPTY(spec, r.Replay)
	p.IgnoreSIGINT, p.IgnoreSIGTERM, p.Echo = r.IgnoreSIGINT, r.IgnoreSIGTERM, r.Echo
	r.ptys = append(r.ptys, p)
	ch := r.Started
	r.mu.Unlock()
	if ch != nil {
		ch <- p
	}
	return p, nil
}

// AttachPTY implements harness.PTYRunner.
func (r *Runner) AttachPTY(_ context.Context, _ string, id string) (harness.PTY, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.ptys {
		if p.id == id && !p.Exited() {
			return p, nil
		}
	}
	return nil, errors.New("no such pty")
}

// ListPTYs implements harness.PTYRunner.
func (r *Runner) ListPTYs(context.Context, string) ([]harness.PTYInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]harness.PTYInfo, 0, len(r.ptys))
	for _, p := range r.ptys {
		out = append(out, harness.PTYInfo{ID: p.id, Command: p.Spec.Command, Dir: p.Spec.Dir, Running: !p.Exited()})
	}
	return out, nil
}

// PTYs returns every PTY started so far.
func (r *Runner) PTYs() []*PTY {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.ptys)
}

// Last returns the newest PTY.
func (r *Runner) Last() *PTY {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ptys) == 0 {
		return nil
	}
	return r.ptys[len(r.ptys)-1]
}

// Waker is a fake VM controller with lease counting.
type Waker struct {
	mu     sync.Mutex
	Wakes  int
	leases map[string]int
	Err    error
}

// EnsureRunning implements harness.VMWaker.
func (w *Waker) EnsureRunning(context.Context, string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Wakes++
	return w.Err
}

// AcquireLease implements harness.LeaseAcquirer.
func (w *Waker) AcquireLease(_, kind string) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.leases == nil {
		w.leases = map[string]int{}
	}
	w.leases[kind]++
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			w.leases[kind]--
			w.mu.Unlock()
		})
	}
}

// Leases returns the active lease count of kind.
func (w *Waker) Leases(kind string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.leases[kind]
}

// Dirs maps tenants to fixed host directories.
type Dirs struct{ Home, Workspace string }

// HomeDir implements harness.Dirs.
func (d Dirs) HomeDir(string) (string, error) { return d.Home, nil }

// WorkspaceDir implements harness.Dirs.
func (d Dirs) WorkspaceDir(string) (string, error) { return d.Workspace, nil }

// Store is an in-memory harness.SessionStore.
type Store struct {
	mu sync.Mutex
	m  map[string]store.SessionMeta
}

func key(t, h, s string) string { return t + "\x00" + h + "\x00" + s }

// UpsertSessionMeta implements harness.SessionStore.
func (s *Store) UpsertSessionMeta(_ context.Context, tenantID string, m store.SessionMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]store.SessionMeta{}
	}
	now := time.Now().UTC()
	k := key(tenantID, m.Harness, m.SessionID)
	if old, ok := s.m[k]; ok {
		m.CreatedAt = old.CreatedAt
	} else {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	s.m[k] = m
	return nil
}

// ListSessionMeta implements harness.SessionStore.
func (s *Store) ListSessionMeta(_ context.Context, tenantID, h string) ([]store.SessionMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.SessionMeta
	for k, v := range s.m {
		if k[:len(tenantID)+1] == tenantID+"\x00" && (h == "" || v.Harness == h) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// DeleteSessionMeta implements harness.SessionStore.
func (s *Store) DeleteSessionMeta(_ context.Context, tenantID, h, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(tenantID, h, id)
	if _, ok := s.m[k]; !ok {
		return store.ErrNotFound
	}
	delete(s.m, k)
	return nil
}

// Get returns one stored record.
func (s *Store) Get(tenantID, h, id string) (store.SessionMeta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key(tenantID, h, id)]
	return v, ok
}
