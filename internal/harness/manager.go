package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/store"
)

// Session statuses (docs/api.md section 8).
const (
	StatusRunning = "running"
	StatusIdle    = "idle"
	StatusExited  = "exited"
)

// Session is the API view of one harness session.
type Session struct {
	ID           string    `json:"id"`
	Harness      string    `json:"harness"`
	NativeID     string    `json:"nativeId"`
	Title        string    `json:"title"`
	WorkspaceDir string    `json:"workspaceDir"`
	Model        string    `json:"model"`
	Status       string    `json:"status"`
	Live         bool      `json:"live"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Usage        *Usage    `json:"usage,omitempty"`
}

// SessionID joins a harness and a native ID.
func SessionID(harness, nativeID string) string { return harness + ":" + nativeID }

// SplitSessionID splits "<harness>:<nativeId>".
func SplitSessionID(id string) (string, string, error) {
	h, n, ok := strings.Cut(id, ":")
	if !ok || h == "" || n == "" {
		return "", "", fmt.Errorf("%w: session %q", ErrNotFound, id)
	}
	return h, n, nil
}

// SessionStore persists session metadata (implemented by *store.Store).
type SessionStore interface {
	UpsertSessionMeta(ctx context.Context, tenantID string, m store.SessionMeta) error
	ListSessionMeta(ctx context.Context, tenantID, harness string) ([]store.SessionMeta, error)
	DeleteSessionMeta(ctx context.Context, tenantID, harness, sessionID string) error
}

// Wiring points harness configs at the gateway before a start and supplies
// the gateway environment (implemented by *magpie.Bindings).
type Wiring interface {
	EnsureWired(ctx context.Context, tenantID, harness string) error
	Env(harness string) map[string]string
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Adapters []Adapter
	// Files creates workspace directories (agentfs.Local or remotefs).
	Files agentfs.FS
	// Discovery finds and purges sessions (LocalDiscovery or
	// GuestDiscovery).
	Discovery Discovery
	Store     SessionStore
	Runner    PTYRunner
	Waker     VMWaker
	Wiring    Wiring
	Logger    *slog.Logger
	// KillGrace is the wait after SIGINT before SIGTERM (default 3s).
	KillGrace time.Duration
	// TermGrace is the wait after SIGTERM before detaching (default 2s).
	TermGrace   time.Duration
	WakeTimeout time.Duration // default 120s
}

// extra is the JSON stored in sessions_meta.extra_json.
type extra struct {
	PTYID       string    `json:"ptyId,omitempty"`
	Provisional bool      `json:"provisional,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
}

type liveSession struct {
	tenantID    string
	harness     string
	nativeID    string
	workspace   string
	model       string
	title       string
	provisional bool
	ptyID       string
	startedAt   time.Time
	room        *Room
	pty         PTY
	release     func()
	deleted     bool // guarded by Manager.mu; set by Delete
}

// Manager is the unified session manager of all harnesses.
type Manager struct {
	opts     ManagerOptions
	log      *slog.Logger
	adapters map[string]Adapter

	mu      sync.Mutex
	live    map[string]*liveSession // by tenant + "\x00" + session ID
	aliases map[string]string       // provisional key -> adopted key
}

// NewManager validates opts.
func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Files == nil || opts.Discovery == nil || opts.Store == nil {
		return nil, errors.New("harness: Files, Discovery, and Store are required")
	}
	if opts.Runner == nil {
		opts.Runner = UnavailableRunner{}
	}
	if opts.Waker == nil {
		opts.Waker = NoopWaker{}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.KillGrace <= 0 {
		opts.KillGrace = 3 * time.Second
	}
	if opts.TermGrace <= 0 {
		opts.TermGrace = 2 * time.Second
	}
	if opts.WakeTimeout <= 0 {
		opts.WakeTimeout = 120 * time.Second
	}
	m := &Manager{opts: opts, log: opts.Logger.With("component", "harness"), adapters: map[string]Adapter{},
		live: map[string]*liveSession{}, aliases: map[string]string{}}
	for _, a := range opts.Adapters {
		m.adapters[a.Name()] = a
	}
	return m, nil
}

// Adapter returns the adapter of a harness.
func (m *Manager) Adapter(harness string) (Adapter, bool) {
	a, ok := m.adapters[harness]
	return a, ok
}

func liveKey(tenantID, id string) string { return tenantID + "\x00" + id }

func randomID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}

// CreateRequest is the body of POST /api/v1/sessions.
type CreateRequest struct {
	Harness        string `json:"harness"`
	WorkspaceDir   string `json:"workspaceDir"`
	Model          string `json:"model"`
	ResumeNativeID string `json:"resumeNativeId"`
	Title          string `json:"title"`
	Cols           uint16 `json:"cols"`
	Rows           uint16 `json:"rows"`
}

// Create starts a harness terminal in the guest. It starts the VM, also
// one that the user stopped (an explicit request).
func (m *Manager) Create(ctx context.Context, tenantID string, req CreateRequest) (Session, error) {
	ws, err := CleanGuestDir(req.WorkspaceDir)
	if err != nil {
		return Session{}, err
	}
	var cmd []string
	var nativeID string
	switch a, ok := m.adapters[req.Harness]; {
	case req.Harness == Shell:
		cmd, nativeID = []string{"bash", "-l"}, randomID("pty-")
		if req.ResumeNativeID != "" {
			return Session{}, fmt.Errorf("%w: the shell has no sessions to resume", ErrBadRequest)
		}
	case ok:
		if req.ResumeNativeID != "" && strings.ContainsAny(req.ResumeNativeID, "/\\\x00") {
			return Session{}, fmt.Errorf("%w: invalid resumeNativeId", ErrBadRequest)
		}
		cmd, nativeID = a.LaunchCommand(LaunchOptions{WorkspaceDir: ws, Model: req.Model, ResumeNativeID: req.ResumeNativeID})
	default:
		return Session{}, fmt.Errorf("%w: unknown harness %q", ErrBadRequest, req.Harness)
	}
	provisional := nativeID == ""
	if provisional {
		nativeID = randomID("new-")
	}
	if err := m.startVM(ctx, tenantID); err != nil {
		return Session{}, err
	}
	if err := m.ensureWorkspace(ctx, tenantID, ws); err != nil {
		return Session{}, err
	}
	ls := &liveSession{tenantID: tenantID, harness: req.Harness, nativeID: nativeID, workspace: ws, model: req.Model,
		title: req.Title, provisional: provisional}
	if err := m.start(ctx, ls, cmd, req.Cols, req.Rows); err != nil {
		return Session{}, err
	}
	return m.sessionOf(ls), nil
}

// startVM starts the VM for a new or resumed session, an explicit request
// of the user: it also starts a VM that the user stopped (VMStarter).
func (m *Manager) startVM(ctx context.Context, tenantID string) error {
	st, ok := m.opts.Waker.(VMStarter)
	if !ok {
		return nil
	}
	wctx, cancel := context.WithTimeout(ctx, m.opts.WakeTimeout)
	defer cancel()
	if err := st.Start(wctx, tenantID); err != nil {
		if errors.Is(err, ErrUnavailable) {
			return err
		}
		return fmt.Errorf("%w: start VM: %w", ErrUnavailable, err)
	}
	return nil
}

func (m *Manager) ensureWorkspace(ctx context.Context, tenantID, guestDir string) error {
	root, rel, err := guestRoot(guestDir)
	if err != nil {
		return err
	}
	if err := m.opts.Files.MkdirAll(ctx, tenantID, root, rel); err != nil {
		if errors.Is(err, agentfs.ErrUnavailable) {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return fmt.Errorf("%w: workspace %s: %v", ErrBadRequest, guestDir, err)
	}
	return nil
}

// start wakes the VM, starts the PTY, and registers the live session.
func (m *Manager) start(ctx context.Context, ls *liveSession, cmd []string, cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		cols, rows = 160, 48
	}
	if ls.harness != Shell && m.opts.Wiring != nil {
		if err := m.opts.Wiring.EnsureWired(ctx, ls.tenantID, ls.harness); err != nil {
			m.log.Warn("harness config wiring failed; starting with the existing config", "harness", ls.harness, "err", err)
		}
	}
	wctx, cancel := context.WithTimeout(ctx, m.opts.WakeTimeout)
	defer cancel()
	if err := m.opts.Waker.EnsureRunning(wctx, ls.tenantID); err != nil {
		if errors.Is(err, ErrUnavailable) {
			return err
		}
		return fmt.Errorf("%w: wake VM: %w", ErrUnavailable, err)
	}
	if la, ok := m.opts.Waker.(LeaseAcquirer); ok {
		ls.release = la.AcquireLease(ls.tenantID, "pty")
	}
	id := SessionID(ls.harness, ls.nativeID)
	env := map[string]string{"TERM": "xterm-256color", "LYDIABOX_SESSION_ID": id, "LYDIABOX_HARNESS": ls.harness}
	if m.opts.Wiring != nil && ls.harness != Shell {
		maps.Copy(env, m.opts.Wiring.Env(ls.harness))
	}
	ls.ptyID = randomID("lbx-")
	pty, err := m.opts.Runner.StartPTY(ctx, ls.tenantID, PTYSpec{ID: ls.ptyID, Command: cmd, Dir: ls.workspace, Env: env, Cols: cols, Rows: rows})
	if err != nil {
		if ls.release != nil {
			ls.release()
		}
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBadRequest) {
			return err
		}
		return fmt.Errorf("%w: start terminal: %v", ErrUnavailable, err)
	}
	ls.pty = pty
	ls.startedAt = time.Now().UTC()
	ls.room = newRoom(id, pty, cols, rows, m.heldFunc(ls.tenantID), func(code int) { m.exited(ls, code) })
	m.mu.Lock()
	m.live[liveKey(ls.tenantID, id)] = ls
	m.mu.Unlock()
	m.persist(context.WithoutCancel(ctx), ls, StatusRunning)
	m.log.Info("harness session started", "session", id, "workspace", ls.workspace, "pty", ls.ptyID)
	return nil
}

func (m *Manager) persist(ctx context.Context, ls *liveSession, status string) {
	x, _ := json.Marshal(extra{PTYID: ls.ptyID, Provisional: ls.provisional, StartedAt: ls.startedAt})
	err := m.opts.Store.UpsertSessionMeta(ctx, ls.tenantID, store.SessionMeta{Harness: ls.harness, SessionID: ls.nativeID,
		WorkspaceDir: ls.workspace, Title: ls.title, Model: ls.model, Status: status, Extra: x})
	if err != nil {
		m.log.Warn("persist session metadata", "harness", ls.harness, "session", ls.nativeID, "err", err)
	}
}

// exited runs when a PTY process ends.
func (m *Manager) exited(ls *liveSession, code int) {
	m.mu.Lock()
	id := SessionID(ls.harness, ls.nativeID)
	if cur := m.live[liveKey(ls.tenantID, id)]; cur == ls {
		delete(m.live, liveKey(ls.tenantID, id))
	}
	deleted := ls.deleted
	m.mu.Unlock()
	if ls.release != nil {
		ls.release()
		ls.release = nil
	}
	if !deleted {
		m.persist(context.Background(), ls, StatusExited)
	}
	m.log.Info("harness session exited", "session", id, "code", code)
}

// lookupLive returns the live session for an API ID (following aliases).
func (m *Manager) lookupLive(tenantID, id string) *liveSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := liveKey(tenantID, id)
	if to, ok := m.aliases[k]; ok {
		k = to
	}
	return m.live[k]
}

// stop interrupts a live session: SIGINT, KillGrace, SIGTERM, TermGrace,
// SIGHUP, TermGrace, then detach.
func (m *Manager) stop(ctx context.Context, ls *liveSession) {
	pty := ls.pty
	_ = pty.Signal("SIGINT")
	if !waitExit(ctx, pty, m.opts.KillGrace) {
		_ = pty.Signal("SIGTERM")
		// An interactive shell ignores SIGINT and SIGTERM but exits on a
		// terminal hangup.
		if !waitExit(ctx, pty, m.opts.TermGrace) {
			_ = pty.Signal("SIGHUP")
			if !waitExit(ctx, pty, m.opts.TermGrace) {
				m.log.Warn("harness did not exit after SIGHUP; detaching", "session", SessionID(ls.harness, ls.nativeID))
			}
		}
	}
	_ = pty.Close()
}

func waitExit(ctx context.Context, pty PTY, d time.Duration) bool {
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	_, err := pty.Wait(wctx)
	return err == nil
}

// Delete terminates the session terminal (if any), removes host metadata,
// and with purge also deletes the native transcript.
func (m *Manager) Delete(ctx context.Context, tenantID, id string, purge bool) error {
	harness, nativeID, err := SplitSessionID(id)
	if err != nil {
		return err
	}
	ls := m.lookupLive(tenantID, id)
	if ls == nil {
		if _, err := m.Get(ctx, tenantID, id); err != nil {
			return err
		}
	} else {
		harness, nativeID = ls.harness, ls.nativeID
		m.mu.Lock()
		ls.deleted = true
		m.mu.Unlock()
		m.stop(ctx, ls)
		m.mu.Lock()
		delete(m.live, liveKey(tenantID, SessionID(harness, nativeID)))
		m.mu.Unlock()
	}
	if err := m.opts.Store.DeleteSessionMeta(ctx, tenantID, harness, nativeID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if purge {
		if _, ok := m.adapters[harness]; !ok {
			return nil
		}
		if err := m.opts.Discovery.Purge(ctx, tenantID, harness, nativeID); err != nil {
			return fmt.Errorf("purge %s: %w", id, err)
		}
	}
	return nil
}

// Room returns the terminal room of a session for a WebSocket client. A
// terminal that survived a host restart is re-attached from the guest.
func (m *Manager) Room(ctx context.Context, tenantID, id string) (*Room, error) {
	if ls := m.lookupLive(tenantID, id); ls != nil {
		return ls.room, nil
	}
	harness, nativeID, err := SplitSessionID(id)
	if err != nil {
		return nil, err
	}
	metas, err := m.opts.Store.ListSessionMeta(ctx, tenantID, harness)
	if err != nil {
		return nil, err
	}
	for _, sm := range metas {
		if sm.SessionID != nativeID || sm.Status != StatusRunning {
			continue
		}
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		if x.PTYID == "" {
			break
		}
		if err := m.opts.Waker.EnsureRunning(ctx, tenantID); err != nil {
			// A VM that the user stopped is not started for a reconnect.
			return nil, fmt.Errorf("%w: wake VM: %w", ErrUnavailable, err)
		}
		pty, err := m.opts.Runner.AttachPTY(ctx, tenantID, x.PTYID)
		if err != nil {
			break
		}
		ls := &liveSession{tenantID: tenantID, harness: harness, nativeID: nativeID, workspace: sm.WorkspaceDir,
			model: sm.Model, title: sm.Title, provisional: x.Provisional, ptyID: x.PTYID, startedAt: x.StartedAt, pty: pty}
		if la, ok := m.opts.Waker.(LeaseAcquirer); ok {
			ls.release = la.AcquireLease(tenantID, "pty")
		}
		ls.room = newRoom(id, pty, 160, 48, m.heldFunc(tenantID), func(code int) { m.exited(ls, code) })
		m.mu.Lock()
		m.live[liveKey(tenantID, id)] = ls
		m.mu.Unlock()
		return ls.room, nil
	}
	if _, err := m.Get(ctx, tenantID, id); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: session %s has no running terminal; start it with POST /api/v1/sessions and resumeNativeId", ErrConflict, id)
}

// heldFunc reports whether the user stopped the VM of tenantID, when the
// waker knows it (VMHolder).
func (m *Manager) heldFunc(tenantID string) func() bool {
	h, ok := m.opts.Waker.(VMHolder)
	if !ok {
		return nil
	}
	return func() bool { return h.Held(tenantID) }
}

func (m *Manager) sessionOf(ls *liveSession) Session {
	now := ls.startedAt
	return Session{ID: SessionID(ls.harness, ls.nativeID), Harness: ls.harness, NativeID: ls.nativeID, Title: ls.title,
		WorkspaceDir: ls.workspace, Model: ls.model, Status: StatusRunning, Live: true, CreatedAt: now, UpdatedAt: now}
}
