//go:build unix

// Package ptymanager runs the guest's persistent PTY sessions (harnesses and
// the web terminal). Each session keeps a 1 MiB ring buffer of output that
// is replayed to every client that attaches, and any number of clients can
// attach at the same time (a terminal room).
package ptymanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// GatewayURL is the in-guest LLM gateway (credproxy listens here).
const GatewayURL = "http://127.0.0.1:3425"

// DefaultEnv returns the variables injected into every PTY: the host broker
// address used by `lydiabox-guest git-cred`, and the base URLs that route
// harness LLM traffic through the in-guest gateway. Both base URLs are the
// gateway root; the gateway adds /v1 to OpenAI-style paths without it.
func DefaultEnv(hostAddr string) map[string]string {
	return map[string]string{
		rpc.EnvHostAddr:      hostAddr,
		"ANTHROPIC_BASE_URL": GatewayURL,
		"OPENAI_BASE_URL":    GatewayURL,
		// Gemini SDKs (and agy with "modelProvider": "gemini") use these;
		// the host side of the tunnel replaces the placeholder key.
		"GOOGLE_GEMINI_BASE_URL": GatewayURL,
		"GEMINI_API_KEY":         "lydiabox-local",
		"TERM":                   "xterm-256color",
		"COLORTERM":              "truecolor",
	}
}

// Options configures a Manager.
type Options struct {
	// Shell is the command used when a spec has no command (default: $SHELL
	// or /bin/bash, as a login shell).
	Shell string
	// DefaultDir is the working directory when a spec has none.
	DefaultDir string
	// Dirs maps a guest directory (for example /workspace) to the local
	// directory that holds it, for a guest whose trees are elsewhere and
	// not linked (a test guest). A spec directory below a key moves there.
	Dirs map[string]string
	// Env is added to every PTY environment after the inherited part of
	// the daemon's own environment (Inherited) and before the spec's
	// variables.
	Env map[string]string
	// RingSize is the replay buffer size (default 1 MiB).
	RingSize int
	// MaxExited is the number of exited sessions kept for late attach and
	// listing (default 32).
	MaxExited int
	Logger    *slog.Logger
}

// Spec describes a PTY to start.
type Spec struct {
	ID      string
	Command []string
	Dir     string
	Env     map[string]string
	Cols    uint16
	Rows    uint16
}

// Manager owns all PTY sessions.
type Manager struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*Session
	exited   []string
	closed   bool
}

// New returns a manager.
func New(opts Options) *Manager {
	if opts.Shell == "" {
		opts.Shell = os.Getenv("SHELL")
		if opts.Shell == "" {
			opts.Shell = "/bin/bash"
		}
	}
	if opts.RingSize <= 0 {
		opts.RingSize = DefaultRingSize
	}
	if opts.MaxExited <= 0 {
		opts.MaxExited = 32
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{opts: opts, sessions: map[string]*Session{}}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Start launches a PTY session.
func (m *Manager) Start(spec Spec) (*Session, error) {
	id := spec.ID
	if id == "" {
		id = newID()
	}
	if strings.ContainsAny(id, "/\x00") || len(id) > 200 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid PTY id %q", id)
	}
	command := spec.Command
	if len(command) == 0 {
		command = []string{m.opts.Shell, "-l"}
	}
	dir := m.localDir(spec.Dir)
	if dir == "" {
		dir = m.opts.DefaultDir
	}
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "working directory %q does not exist", dir)
		}
	}
	cols, rows := spec.Cols, spec.Rows
	if cols == 0 {
		cols = 120
	}
	if rows == 0 {
		rows = 32
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, rpc.Errorf(rpc.CodeUnavailable, "PTY manager is shutting down")
	}
	if old, ok := m.sessions[id]; ok {
		select {
		case <-old.done:
			m.forgetLocked(id)
		default:
			return nil, rpc.Errorf(rpc.CodeConflict, "PTY %q is already running", id)
		}
	}

	path, err := exec.LookPath(command[0])
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "command %q not found", command[0])
	}
	cmd := exec.Command(path, command[1:]...)
	cmd.Args[0] = command[0]
	cmd.Dir = dir
	cmd.Env = m.environ(spec.Env, id)
	ptmx, err := pty.StartWithAttrs(cmd, nil, &syscall.SysProcAttr{Setsid: true, Setctty: true})
	if err != nil {
		return nil, fmt.Errorf("ptymanager: start %s: %w", command[0], err)
	}
	f, err := pollable(ptmx)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	s := &Session{
		id: id, command: append([]string(nil), command...), dir: dir, startedAt: time.Now().UTC(),
		ptmx: f, cmd: cmd, done: make(chan struct{}),
		ring: NewRing(m.opts.RingSize), subs: map[*Subscriber]struct{}{}, cols: cols, rows: rows,
	}
	if err := setWinsize(f, cols, rows); err != nil {
		m.opts.Logger.Warn("set PTY size", "id", id, "err", err)
	}
	m.sessions[id] = s
	readerDone := make(chan struct{})
	go s.readLoop(readerDone)
	go s.waitLoop(readerDone, m.onExit)
	m.opts.Logger.Info("pty started", "id", id, "command", command, "dir", dir)
	return s, nil
}

// pollable re-opens the PTY master as a non-blocking descriptor registered
// with the runtime poller (creack/pty leaves it in blocking mode).
func pollable(ptmx *os.File) (*os.File, error) {
	defer ptmx.Close()
	rc, err := ptmx.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		nfd  int
		derr error
	)
	if err := rc.Control(func(fd uintptr) { nfd, derr = unix.Dup(int(fd)) }); err != nil {
		return nil, err
	}
	if derr != nil {
		return nil, fmt.Errorf("ptymanager: dup PTY: %w", derr)
	}
	unix.CloseOnExec(nfd)
	if err := unix.SetNonblock(nfd, true); err != nil {
		unix.Close(nfd)
		return nil, err
	}
	return os.NewFile(uintptr(nfd), "/dev/ptmx"), nil
}

func (m *Manager) environ(extra map[string]string, id string) []string {
	env := baseEnv(daemonEnviron())
	for k, v := range m.opts.Env {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	env["LYDIABOX_PTY_ID"] = id
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) onExit(s *Session) {
	code, sig := s.Exit()
	m.opts.Logger.Info("pty exited", "id", s.id, "code", code, "signal", sig)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.id] != s {
		return
	}
	m.exited = append(m.exited, s.id)
	for len(m.exited) > m.opts.MaxExited {
		old := m.exited[0]
		m.exited = m.exited[1:]
		if cur, ok := m.sessions[old]; ok && !cur.Info().Running {
			delete(m.sessions, old)
		}
	}
}

func (m *Manager) forgetLocked(id string) {
	delete(m.sessions, id)
	for i, e := range m.exited {
		if e == id {
			m.exited = append(m.exited[:i], m.exited[i+1:]...)
			break
		}
	}
}

// Get returns a session by ID.
func (m *Manager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, rpc.Errorf(rpc.CodeNotFound, "PTY %q not found", id)
	}
	return s, nil
}

// List describes every session, oldest first.
func (m *Manager) List() []rpc.PTYInfo {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	out := make([]rpc.PTYInfo, 0, len(all))
	for _, s := range all {
		out = append(out, s.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Running returns the number of running sessions.
func (m *Manager) Running() int {
	n := 0
	for _, info := range m.List() {
		if info.Running {
			n++
		}
	}
	return n
}

// Kill terminates a session (SIGTERM to its process group, SIGKILL after
// 3 s) and forgets it.
func (m *Manager) Kill(ctx context.Context, id string) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	terminate(ctx, s)
	m.mu.Lock()
	if m.sessions[id] == s {
		m.forgetLocked(id)
	}
	m.mu.Unlock()
	return nil
}

func terminate(ctx context.Context, s *Session) {
	select {
	case <-s.done:
		return
	default:
	}
	_ = s.signalGroup(syscall.SIGHUP)
	_ = s.signalGroup(syscall.SIGTERM)
	t := time.NewTimer(3 * time.Second)
	defer t.Stop()
	select {
	case <-s.done:
		return
	case <-t.C:
	case <-ctx.Done():
	}
	_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	_ = s.cmd.Process.Kill()
	<-s.done
}

// Close terminates every session.
func (m *Manager) Close(ctx context.Context) {
	m.mu.Lock()
	m.closed = true
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			terminate(ctx, s)
		}()
	}
	wg.Wait()
}

// localDir applies Options.Dirs to a spec directory.
func (m *Manager) localDir(dir string) string {
	for guest, local := range m.opts.Dirs {
		if dir == guest {
			return local
		}
		if rest, ok := strings.CutPrefix(dir, guest+"/"); ok {
			return local + "/" + rest
		}
	}
	return dir
}
