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
	ID           string `json:"id"`
	Harness      string `json:"harness"`
	NativeID     string `json:"nativeId"`
	Title        string `json:"title"`
	WorkspaceDir string `json:"workspaceDir"`
	Model        string `json:"model"`
	Status       string `json:"status"`
	Live         bool   `json:"live"`
	// Engine is how the session runs or last ran: EnginePTY (the harness
	// TUI in a terminal) or EngineACP (the chat view through the bridge).
	Engine    string    `json:"engine,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Usage     *Usage    `json:"usage,omitempty"`
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

// SessionDismisser keeps the discovered sessions that the user deleted
// without their transcript (implemented by *store.Store). Discovery finds
// such a session again in its transcript; the list leaves it out until the
// transcript has newer activity.
type SessionDismisser interface {
	DismissSession(ctx context.Context, tenantID, harness, sessionID string) error
	UndismissSession(ctx context.Context, tenantID, harness, sessionID string) error
	ListDismissedSessions(ctx context.Context, tenantID string) ([]store.DismissedSession, error)
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
	TermGrace time.Duration
	// WakeTimeout bounds the wait of an HTTP request for a VM start
	// (default 75 s, below the 100 s after which a proxy such as
	// Cloudflare ends a request). A longer start continues; the request
	// fails with an error that says that the VM still starts.
	WakeTimeout time.Duration
	// EventPoll is how often the event stream of a live session reads its
	// transcript and terminal state (default 1s; four times as long for a
	// session without a terminal).
	EventPoll time.Duration
	// MCPServers are passed to the ACP agents (wire.DefaultMCPServers).
	MCPServers []MCPServer
	// EventCache keeps a host copy of the session events (provider storage
	// mode only; nil turns it off). EventCopyDelay is the shortest time
	// between two writes of the copy of a watched session (default 15s).
	EventCache     EventCache
	EventCopyDelay time.Duration
}

// extra is the JSON stored in sessions_meta.extra_json.
type extra struct {
	PTYID       string    `json:"ptyId,omitempty"`
	Provisional bool      `json:"provisional,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	// EndedAt is when the terminal ended (exited sessions).
	EndedAt time.Time `json:"endedAt,omitzero"`
	// Lost reports that the terminal ended with the VM (markExited).
	Lost bool `json:"lost,omitempty"`
	// Engine is EngineACP for a session that runs through the bridge.
	Engine string `json:"engine,omitempty"`
}

type liveSession struct {
	tenantID    string
	harness     string
	nativeID    string
	workspace   string
	model       string
	title       string
	provisional bool
	engine      string
	ptyID       string
	startedAt   time.Time
	endedAt     time.Time
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
	// starting holds the guest PTY IDs of starts in progress, so that
	// Reconcile does not take a new terminal for an orphan.
	starting map[string]bool

	reconcileMu sync.Mutex
	closed      bool // guarded by reconcileMu; set by Close

	hubsMu sync.Mutex
	hubs   map[string]*eventHub // by liveKey of the requested session ID

	copies eventCopies
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
		opts.WakeTimeout = DefaultWakeTimeout
	}
	m := &Manager{opts: opts, log: opts.Logger.With("component", "harness"), adapters: map[string]Adapter{},
		live: map[string]*liveSession{}, aliases: map[string]string{}, starting: map[string]bool{}}
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
	// Engine is EnginePTY (default) or EngineACP.
	Engine string `json:"engine"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
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
	engine, err := checkEngine(req.Harness, req.Engine)
	if err != nil {
		return Session{}, err
	}
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
		if engine == EngineACP {
			cmd, nativeID = m.acpCommand(req.Harness, ws, req.Model, req.ResumeNativeID), req.ResumeNativeID
		} else {
			cmd, nativeID = a.LaunchCommand(LaunchOptions{WorkspaceDir: ws, Model: req.Model, ResumeNativeID: req.ResumeNativeID})
		}
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
		title: req.Title, provisional: provisional, engine: engine}
	if err := m.start(ctx, ls, cmd, req.Cols, req.Rows); err != nil {
		return Session{}, err
	}
	if dis, ok := m.opts.Store.(SessionDismisser); ok && req.ResumeNativeID != "" {
		// A resumed session is wanted again.
		if err := dis.UndismissSession(context.WithoutCancel(ctx), tenantID, req.Harness, nativeID); err != nil {
			m.log.Warn("drop a session dismissal", "session", SessionID(req.Harness, nativeID), "err", err)
		}
	}
	return m.sessionOf(ls), nil
}

// DefaultWakeTimeout is the default of Options.WakeTimeout.
const DefaultWakeTimeout = 75 * time.Second

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
	m.wire(ctx, ls)
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
	m.mu.Lock()
	m.starting[ls.ptyID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.starting, ls.ptyID)
		m.mu.Unlock()
	}()
	pty, err := m.opts.Runner.StartPTY(ctx, ls.tenantID, PTYSpec{ID: ls.ptyID, Command: cmd, Dir: ls.workspace, Env: env, Cols: cols, Rows: rows})
	if err != nil {
		if ls.release != nil {
			ls.release()
		}
		if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrBadRequest) {
			return err
		}
		if commandNotFound(err) {
			m.log.Info("harness not installed in the VM", "harness", ls.harness, "command", cmd[0])
			return &NotInstalledError{Harness: ls.harness, Command: cmd[0]}
		}
		return fmt.Errorf("%w: start terminal: %v", ErrUnavailable, err)
	}
	ls.pty = pty
	ls.startedAt = time.Now().UTC()
	ls.room = newRoom(id, pty, cols, rows, m.roomVM(ls.tenantID), m.roomEnd(ls))
	m.mu.Lock()
	m.live[liveKey(ls.tenantID, id)] = ls
	m.mu.Unlock()
	m.persist(context.WithoutCancel(ctx), ls, StatusRunning)
	m.log.Info("harness session started", "session", id, "workspace", ls.workspace, "pty", ls.ptyID)
	return nil
}

func (m *Manager) persist(ctx context.Context, ls *liveSession, status string) {
	x, _ := json.Marshal(extra{PTYID: ls.ptyID, Provisional: ls.provisional, StartedAt: ls.startedAt, EndedAt: ls.endedAt, Engine: ls.engine})
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
		ls.endedAt = time.Now().UTC()
		m.persist(context.Background(), ls, StatusExited)
	}
	m.log.Info("harness session exited", "session", id, "code", code)
}

// Paused ends the live terminals of tenantID with "paused" after its VM
// paused. Their processes keep running in the paused guest; a reconnect
// after the wake attaches to them again.
func (m *Manager) Paused(tenantID string) {
	m.mu.Lock()
	var rooms []*Room
	for _, ls := range m.live {
		if ls.tenantID == tenantID && ls.room != nil {
			rooms = append(rooms, ls.room)
		}
	}
	m.mu.Unlock()
	for _, r := range rooms {
		r.EndPaused()
	}
}

// detached runs when a pause of the VM cut the stream of a live terminal.
// The session stays running in the store with its PTY ID, so the next
// Room call wakes the VM and attaches to the same PTY.
func (m *Manager) detached(ls *liveSession) {
	m.mu.Lock()
	id := SessionID(ls.harness, ls.nativeID)
	if cur := m.live[liveKey(ls.tenantID, id)]; cur == ls {
		delete(m.live, liveKey(ls.tenantID, id))
	}
	m.mu.Unlock()
	if ls.release != nil {
		ls.release()
		ls.release = nil
	}
	_ = ls.pty.Close()
	m.log.Info("harness terminal detached; the session stays running", "session", id)
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
		// A terminal that is not attached (after a host restart or a
		// pause) still runs in the guest: end it.
		m.killStored(ctx, tenantID, harness, nativeID)
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
	// The stored events go with the session, also when the transcript
	// stays (a dismissal).
	m.dropEventCopy(tenantID, SessionID(harness, nativeID))
	if _, ok := m.adapters[harness]; !ok {
		return nil
	}
	dis, canDismiss := m.opts.Store.(SessionDismisser)
	if !purge {
		// The transcript stays, and discovery would list the session
		// again: record that the user deleted it.
		if canDismiss {
			if err := dis.DismissSession(context.WithoutCancel(ctx), tenantID, harness, nativeID); err != nil {
				return err
			}
		}
		return nil
	}
	if err := m.opts.Discovery.Purge(ctx, tenantID, harness, nativeID); err != nil {
		// Not dismissed: the session stays in the list, so the purge can
		// be tried again.
		return fmt.Errorf("purge %s: %w", id, err)
	}
	if canDismiss {
		// The transcript is gone; a dismissal of an earlier delete is not needed.
		if err := dis.UndismissSession(context.WithoutCancel(ctx), tenantID, harness, nativeID); err != nil {
			m.log.Warn("drop a session dismissal", "session", id, "err", err)
		}
	}
	return nil
}

// roomVM reports the state of the VM of tenantID to a room, as far as the
// waker knows it (VMHolder, VMPauseReporter).
func (m *Manager) roomVM(tenantID string) roomVM {
	var vm roomVM
	if h, ok := m.opts.Waker.(VMHolder); ok {
		vm.stopped = func() bool { return h.Held(tenantID) }
	}
	if p, ok := m.opts.Waker.(VMPauseReporter); ok {
		vm.paused = func() bool { return p.Paused(tenantID) }
	}
	return vm
}

func (m *Manager) roomEnd(ls *liveSession) roomEnd {
	return roomEnd{exited: func(code int) { m.exited(ls, code) }, detached: func() { m.detached(ls) }}
}

func (m *Manager) sessionOf(ls *liveSession) Session {
	now := ls.startedAt
	return Session{ID: SessionID(ls.harness, ls.nativeID), Harness: ls.harness, NativeID: ls.nativeID, Title: ls.title,
		WorkspaceDir: ls.workspace, Model: ls.model, Status: StatusRunning, Live: true, Engine: engineName(ls.engine), CreatedAt: now, UpdatedAt: now}
}
