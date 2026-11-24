package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/portwatcher/lydiabox/internal/store"
)

// GuestBinary is the guest program that runs the ACP bridge.
const GuestBinary = "lydiabox-guest"

// checkEngine validates the engine of a start; "" is EnginePTY. A shell
// runs only in a terminal.
func checkEngine(h, engine string) (string, error) {
	switch engine {
	case "", EnginePTY:
		return EnginePTY, nil
	case EngineACP:
		switch h {
		case Shell:
			return "", fmt.Errorf("%w: a shell runs only in a terminal", ErrBadRequest)
		case Antigravity:
			// agy acp (1.5.9) offers only Google's built-in models and
			// ignores the custom gateway model (no --model flag; the CLI
			// settings key is not read), so it cannot use the model that
			// LydiaBox binds (docs/harness-formats.md, "ACP agents").
			return "", fmt.Errorf("%w: the Antigravity ACP agent cannot use the LydiaBox model gateway; run Antigravity in the terminal", ErrBadRequest)
		}
		return EngineACP, nil
	}
	return "", fmt.Errorf("%w: engine must be %q or %q", ErrBadRequest, EnginePTY, EngineACP)
}

func engineName(e string) string {
	if e == EngineACP {
		return EngineACP
	}
	return EnginePTY
}

// MCPServer is an HTTP MCP server that the ACP bridge passes to the agent.
type MCPServer struct {
	Name string
	URL  string
}

// acpCommand is the guest command line of the ACP bridge of a harness. The
// bridge passes the MCP servers to the agent in session/new, so every
// harness gets them the same way.
func (m *Manager) acpCommand(h, workspace, model, resume string) []string {
	cmd := []string{GuestBinary, "acp", "--harness", h, "--cwd", workspace}
	if resume != "" {
		cmd = append(cmd, "--resume", resume)
	}
	if model != "" {
		cmd = append(cmd, "--model", model)
	}
	for _, s := range m.opts.MCPServers {
		cmd = append(cmd, "--mcp", s.Name+"="+s.URL)
	}
	return cmd
}

// adoptNative gives a live ACP session the native ID that its agent
// reported.
func (m *Manager) adoptNative(tenantID, id, nativeID string) {
	if nativeID == "" {
		return
	}
	m.mu.Lock()
	key := liveKey(tenantID, id)
	if to, ok := m.aliases[key]; ok {
		key = to
	}
	ls := m.live[key]
	if ls == nil || ls.nativeID == nativeID {
		m.mu.Unlock()
		return
	}
	if cur := m.live[liveKey(tenantID, SessionID(ls.harness, nativeID))]; cur != nil {
		m.mu.Unlock()
		m.log.Warn("ACP session reported a native ID that another session has", "session", id, "native", nativeID)
		return
	}
	a := m.renameLocked(key, ls, nativeID)
	m.mu.Unlock()
	ctx := context.Background()
	if err := m.opts.Store.DeleteSessionMeta(ctx, tenantID, a.harness, a.oldNative); err != nil && !errors.Is(err, store.ErrNotFound) {
		m.log.Warn("drop provisional session metadata", "session", a.oldNative, "err", err)
	}
	m.persist(ctx, &a.snapshot, StatusRunning)
}

// EngineRequest is the body of PATCH /api/v1/sessions/{id} that changes the
// engine.
type EngineRequest struct {
	Engine string `json:"engine"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
}

// SetEngine continues a live session in the other engine: it ends the
// terminal or the bridge and resumes the same native session in the other
// one. Only one front end runs a session at a time.
func (m *Manager) SetEngine(ctx context.Context, tenantID, id string, req EngineRequest) (Session, error) {
	ls := m.lookupLive(tenantID, id)
	if ls == nil {
		if _, err := m.Get(ctx, tenantID, id); err != nil {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("%w: session %s is not running; continue it with the engine instead", ErrConflict, id)
	}
	engine, err := checkEngine(ls.harness, req.Engine)
	if err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	cur, provisional := engineName(ls.engine), ls.provisional
	m.mu.Unlock()
	switch {
	case cur == engine:
		return m.sessionOf(ls), nil
	case provisional:
		return Session{}, fmt.Errorf("%w: the harness has not saved this session yet; send a first message, then switch", ErrConflict)
	}
	m.mu.Lock()
	ls.deleted = true // the old process ends without an "exited" record
	m.mu.Unlock()
	m.stop(ctx, ls)
	key := liveKey(tenantID, SessionID(ls.harness, ls.nativeID))
	m.mu.Lock()
	if m.live[key] == ls {
		delete(m.live, key)
	}
	m.mu.Unlock()
	cols, rows := req.Cols, req.Rows
	if (cols == 0 || rows == 0) && ls.room != nil {
		cols, rows = ls.room.Size()
	}
	return m.Create(ctx, tenantID, CreateRequest{Harness: ls.harness, WorkspaceDir: ls.workspace, Model: ls.model,
		ResumeNativeID: ls.nativeID, Title: ls.title, Engine: engine, Cols: cols, Rows: rows})
}
