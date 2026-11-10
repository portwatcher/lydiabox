package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/store"
)

// Filter narrows List. Workspace matches the directory and its children.
type Filter struct {
	Harness   string
	Workspace string
}

func (f Filter) match(s Session) bool {
	if f.Harness != "" && s.Harness != f.Harness {
		return false
	}
	if f.Workspace != "" {
		w := strings.TrimRight(f.Workspace, "/")
		if s.WorkspaceDir != w && !strings.HasPrefix(s.WorkspaceDir, w+"/") {
			return false
		}
	}
	return true
}

// provisionalMatchSlack tolerates clock skew between host and guest when a
// harness-assigned session is matched to its provisional terminal.
const provisionalMatchSlack = 10 * time.Second

// List discovers sessions of every harness from the host home directory
// (no VM needed), merges stored metadata and live terminals, and returns
// them newest first.
func (m *Manager) List(ctx context.Context, tenantID string, f Filter) ([]Session, error) {
	if f.Harness != "" && f.Harness != Shell {
		if _, ok := m.adapters[f.Harness]; !ok {
			return nil, fmt.Errorf("%w: unknown harness %q", ErrBadRequest, f.Harness)
		}
	}
	home, err := m.opts.Dirs.HomeDir(tenantID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*Session{}
	var order []string
	put := func(s Session) *Session {
		if cur, ok := byID[s.ID]; ok {
			return cur
		}
		byID[s.ID] = &s
		order = append(order, s.ID)
		return &s
	}
	discovered := map[string][]SessionMeta{}
	for _, name := range Harnesses {
		a, ok := m.adapters[name]
		if !ok || (f.Harness != "" && f.Harness != name) {
			continue
		}
		metas, err := a.DiscoverSessions(home)
		if err != nil {
			m.log.Warn("session discovery failed", "harness", name, "err", err)
			continue
		}
		discovered[name] = metas
		for _, sm := range metas {
			put(Session{ID: SessionID(name, sm.NativeID), Harness: name, NativeID: sm.NativeID, Title: sm.Title,
				WorkspaceDir: sm.WorkspaceDir, Model: sm.Model, Status: StatusIdle, CreatedAt: sm.CreatedAt,
				UpdatedAt: sm.UpdatedAt, Usage: sm.Usage})
		}
	}
	for _, a := range m.adoptProvisional(tenantID, discovered) {
		if err := m.opts.Store.DeleteSessionMeta(ctx, tenantID, a.harness, a.oldNative); err != nil && !errors.Is(err, store.ErrNotFound) {
			m.log.Warn("drop provisional session metadata", "session", a.oldNative, "err", err)
		}
		m.persist(ctx, &a.snapshot, StatusRunning)
	}

	stored, err := m.opts.Store.ListSessionMeta(ctx, tenantID, f.Harness)
	if err != nil {
		return nil, err
	}
	for _, sm := range stored {
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		id := SessionID(sm.Harness, sm.SessionID)
		if x.Provisional && byID[id] == nil && m.adoptedFrom(tenantID, id) {
			continue
		}
		s := put(Session{ID: id, Harness: sm.Harness, NativeID: sm.SessionID, WorkspaceDir: sm.WorkspaceDir,
			Status: StatusIdle, CreatedAt: sm.CreatedAt, UpdatedAt: sm.UpdatedAt})
		if sm.Title != "" {
			s.Title = sm.Title
		}
		if s.Model == "" {
			s.Model = sm.Model
		}
		if s.WorkspaceDir == "" {
			s.WorkspaceDir = sm.WorkspaceDir
		}
		if sm.Status == StatusExited || sm.Status == StatusRunning {
			// "running" without a live terminal means the host restarted;
			// the guest terminal may still exist (Room re-attaches it).
			s.Status = sm.Status
		}
		if sm.UpdatedAt.After(s.UpdatedAt) {
			s.UpdatedAt = sm.UpdatedAt
		}
	}
	m.mu.Lock()
	for _, ls := range m.live {
		if ls.tenantID != tenantID {
			continue
		}
		s := put(m.sessionOf(ls))
		s.Status, s.Live = StatusRunning, true
		if s.Title == "" {
			s.Title = ls.title
		}
		if s.Model == "" {
			s.Model = ls.model
		}
	}
	m.mu.Unlock()

	out := make([]Session, 0, len(order))
	for _, id := range order {
		s := *byID[id]
		if s.Status == StatusRunning && !s.Live {
			s.Status = StatusIdle
		}
		if f.match(s) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Get returns one session.
func (m *Manager) Get(ctx context.Context, tenantID, id string) (Session, error) {
	h, _, err := SplitSessionID(id)
	if err != nil {
		return Session{}, err
	}
	if ls := m.lookupLive(tenantID, id); ls != nil {
		id = SessionID(ls.harness, ls.nativeID)
	}
	if h != Shell {
		if _, ok := m.adapters[h]; !ok {
			return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, id)
		}
	}
	all, err := m.List(ctx, tenantID, Filter{Harness: h})
	if err != nil {
		return Session{}, err
	}
	for _, s := range all {
		if s.ID == id {
			return s, nil
		}
	}
	return Session{}, fmt.Errorf("%w: session %s", ErrNotFound, id)
}

func (m *Manager) adoptedFrom(tenantID, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.aliases[liveKey(tenantID, id)]
	return ok
}

// adoptProvisional gives a live terminal started without a native ID (the
// harness picks it at runtime) the ID of the session it created: the
// newest discovered session of the same harness and workspace that started
// after the terminal and is not claimed by another terminal.
type adoption struct {
	harness   string
	oldNative string
	snapshot  liveSession
}

func (m *Manager) adoptProvisional(tenantID string, discovered map[string][]SessionMeta) []adoption {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []adoption
	claimed := map[string]bool{}
	for _, ls := range m.live {
		claimed[SessionID(ls.harness, ls.nativeID)] = true
	}
	for key, ls := range m.live {
		if ls.tenantID != tenantID || !ls.provisional {
			continue
		}
		var best *SessionMeta
		for i := range discovered[ls.harness] {
			sm := &discovered[ls.harness][i]
			id := SessionID(ls.harness, sm.NativeID)
			if claimed[id] || sm.WorkspaceDir != ls.workspace || sm.CreatedAt.Before(ls.startedAt.Add(-provisionalMatchSlack)) {
				continue
			}
			if best == nil || sm.CreatedAt.Before(best.CreatedAt) {
				best = sm
			}
		}
		if best == nil {
			continue
		}
		oldID, oldNative := SessionID(ls.harness, ls.nativeID), ls.nativeID
		ls.nativeID, ls.provisional = best.NativeID, false
		newID := SessionID(ls.harness, ls.nativeID)
		delete(m.live, key)
		newKey := liveKey(tenantID, newID)
		m.live[newKey] = ls
		m.aliases[key] = newKey
		claimed[newID] = true
		ls.room.mu.Lock()
		if ls.room.sessionID == oldID {
			ls.room.sessionID = newID
		}
		ls.room.mu.Unlock()
		out = append(out, adoption{harness: ls.harness, oldNative: oldNative, snapshot: liveSession{tenantID: ls.tenantID,
			harness: ls.harness, nativeID: ls.nativeID, workspace: ls.workspace, model: ls.model, title: ls.title,
			ptyID: ls.ptyID, startedAt: ls.startedAt}})
		m.log.Info("provisional session adopted its native ID", "from", oldID, "to", newID)
	}
	return out
}
