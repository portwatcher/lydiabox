package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portwatcher/lydiabox/internal/store"
)

// RunningAttacher is implemented by runners that can attach to a guest
// PTY without waking the VM (guestRunner). Reconcile uses it, so that a
// reconcile never resumes a VM that pauses.
type RunningAttacher interface {
	AttachRunningPTY(ctx context.Context, tenantID, id string) (PTY, error)
}

// PTYKiller is implemented by runners that can end a guest PTY by ID
// without an attachment and without waking the VM (guestRunner).
type PTYKiller interface {
	KillPTY(ctx context.Context, tenantID, id string) error
}

// ErrVMPaused is returned by Reconnect for a VM that pauses or is paused.
var ErrVMPaused = errors.New("the VM is hibernating")

// Reconnect is Room for an automatic reconnect of a client: it does not
// wake a VM that pauses or is paused (ErrVMPaused), because the stream
// that the client lost ended with the pause. A live terminal is returned
// as by Room.
func (m *Manager) Reconnect(ctx context.Context, tenantID, id string) (*Room, error) {
	if ls := m.lookupLive(tenantID, id); ls != nil {
		return ls.room, nil
	}
	if p, ok := m.opts.Waker.(VMPauseReporter); ok && p.Paused(tenantID) {
		return nil, ErrVMPaused
	}
	return m.Room(ctx, tenantID, id)
}

// ptyPrefix starts the IDs of the guest PTYs that this manager starts.
const ptyPrefix = "lbx-"

// attachStored attaches a terminal to the guest PTY of a session stored as
// running (after a host restart, a lost stream, or a pause) and makes it
// live. A PTY that the guest no longer has marks the session exited and
// returns an error that wraps ErrNotFound.
func (m *Manager) attachStored(ctx context.Context, tenantID string, sm store.SessionMeta, x extra,
	attach func(ctx context.Context, tenantID, id string) (PTY, error)) (*Room, error) {
	id := SessionID(sm.Harness, sm.SessionID)
	pty, err := attach(ctx, tenantID, x.PTYID)
	if errors.Is(err, ErrNotFound) {
		m.markExited(ctx, tenantID, sm, x, time.Now().UTC(), true)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	ls := &liveSession{tenantID: tenantID, harness: sm.Harness, nativeID: sm.SessionID, workspace: sm.WorkspaceDir,
		model: sm.Model, title: sm.Title, provisional: x.Provisional, ptyID: x.PTYID, startedAt: x.StartedAt, pty: pty, engine: x.Engine}
	m.mu.Lock()
	if cur := m.live[liveKey(tenantID, id)]; cur != nil {
		// Another attach won the race; keep it.
		m.mu.Unlock()
		_ = pty.Close()
		return cur.room, nil
	}
	if la, ok := m.opts.Waker.(LeaseAcquirer); ok {
		ls.release = la.AcquireLease(tenantID, "pty")
	}
	ls.room = newRoom(id, pty, 160, 48, m.roomVM(tenantID), m.roomEnd(ls))
	m.live[liveKey(tenantID, id)] = ls
	m.mu.Unlock()
	m.log.Info("harness terminal attached again", "session", id, "pty", x.PTYID)
	return ls.room, nil
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
		if sm.SessionID != nativeID {
			continue
		}
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		if sm.Status != StatusRunning || x.PTYID == "" {
			if sm.Status == StatusExited && x.Lost {
				return nil, errTerminalLost
			}
			break
		}
		if err := m.opts.Waker.EnsureRunning(ctx, tenantID); err != nil {
			// A VM that the user stopped is not started for a reconnect.
			return nil, fmt.Errorf("%w: wake VM: %w", ErrUnavailable, err)
		}
		room, err := m.attachStored(ctx, tenantID, sm, x, m.opts.Runner.AttachPTY)
		if errors.Is(err, ErrNotFound) {
			return nil, errTerminalLost
		}
		if err != nil {
			break
		}
		return room, nil
	}
	if _, err := m.Get(ctx, tenantID, id); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: session %s has no running terminal; start it with POST /api/v1/sessions and resumeNativeId", ErrConflict, id)
}

// errTerminalLost answers a client that opens the terminal of a session
// whose terminal ended with the VM. Room gives it before and after
// Reconcile recorded the end, so the client always gets the same answer.
var errTerminalLost = fmt.Errorf("%w: the terminal ended while the VM was paused or stopped", ErrConflict)

// markExited records that the terminal of a stored session ended at end.
// lost is true when the terminal ended with the VM (a stop, or a pause
// that did not keep the processes) and not by its own exit.
func (m *Manager) markExited(ctx context.Context, tenantID string, sm store.SessionMeta, x extra, end time.Time, lost bool) {
	x.EndedAt, x.Lost = end.UTC(), lost
	raw, _ := json.Marshal(x)
	sm.Status, sm.Extra = StatusExited, raw
	if err := m.opts.Store.UpsertSessionMeta(context.WithoutCancel(ctx), tenantID, sm); err != nil {
		m.log.Warn("persist session metadata", "harness", sm.Harness, "session", sm.SessionID, "err", err)
		return
	}
	m.log.Info("harness session exited", "session", SessionID(sm.Harness, sm.SessionID), "pty", x.PTYID)
}

// Reconcile brings the stored sessions in line with the PTYs that the
// guest has. Call it while the VM runs (it does not wake the VM):
//   - a session stored as running whose PTY runs is attached again and is
//     live (a host restart or a lost stream does not end a terminal);
//   - a session stored as running whose PTY ended or is gone is exited,
//     with the guest's end time when the guest still knows it;
//   - a running guest PTY that this manager started (ID "lbx-") and that
//     no stored running session and no start in progress refers to is
//     ended: its session was deleted, or recorded as exited (for example
//     by an older host version), while the process still ran.
func (m *Manager) Reconcile(ctx context.Context, tenantID string) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if m.closed {
		return nil
	}
	// The PTYs first: a PTY in this list was registered in m.starting
	// before it started, and it stays in m.starting until m.live and the
	// store have it.
	ptys, err := m.opts.Runner.ListPTYs(ctx, tenantID)
	if err != nil {
		return err
	}
	byID := make(map[string]PTYInfo, len(ptys))
	for _, p := range ptys {
		byID[p.ID] = p
	}
	metas, err := m.opts.Store.ListSessionMeta(ctx, tenantID, "")
	if err != nil {
		return err
	}
	attach := m.opts.Runner.AttachPTY
	if ra, ok := m.opts.Runner.(RunningAttacher); ok {
		attach = ra.AttachRunningPTY
	}
	referenced := map[string]bool{}
	for _, sm := range metas {
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		if x.PTYID == "" {
			continue
		}
		if sm.Status != StatusRunning {
			continue
		}
		referenced[x.PTYID] = true
		if m.lookupLive(tenantID, SessionID(sm.Harness, sm.SessionID)) != nil {
			continue
		}
		info, ok := byID[x.PTYID]
		switch {
		case ok && info.Running:
			if _, err := m.attachStored(ctx, tenantID, sm, x, attach); err != nil && !errors.Is(err, ErrNotFound) {
				m.log.Warn("attach a guest terminal again", "session", SessionID(sm.Harness, sm.SessionID), "err", err)
			}
		case ok && !info.ExitedAt.IsZero():
			m.markExited(ctx, tenantID, sm, x, info.ExitedAt, false)
		default:
			m.markExited(ctx, tenantID, sm, x, time.Now(), true)
		}
	}
	m.mu.Lock()
	busy := make(map[string]bool, len(m.starting)+len(m.live))
	for id := range m.starting {
		busy[id] = true
	}
	for _, ls := range m.live {
		busy[ls.ptyID] = true
	}
	m.mu.Unlock()
	killer, canKill := m.opts.Runner.(PTYKiller)
	for _, p := range ptys {
		if !p.Running || !strings.HasPrefix(p.ID, ptyPrefix) || referenced[p.ID] || busy[p.ID] || !canKill {
			continue
		}
		if err := killer.KillPTY(ctx, tenantID, p.ID); err != nil {
			m.log.Warn("end a guest terminal without a session", "pty", p.ID, "err", err)
			continue
		}
		m.log.Info("ended a guest terminal without a running session", "pty", p.ID, "command", p.Command)
	}
	return nil
}

// Stopped records that the VM of tenantID stopped: every stored running
// session without a live terminal ended with it (the live ones end through
// their rooms).
func (m *Manager) Stopped(ctx context.Context, tenantID string) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if m.closed {
		return
	}
	metas, err := m.opts.Store.ListSessionMeta(ctx, tenantID, "")
	if err != nil {
		m.log.Warn("list sessions after a VM stop", "err", err)
		return
	}
	for _, sm := range metas {
		if sm.Status != StatusRunning || m.lookupLive(tenantID, SessionID(sm.Harness, sm.SessionID)) != nil {
			continue
		}
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		m.markExited(ctx, tenantID, sm, x, time.Now(), true)
	}
}

// Close ends Reconcile and Stopped: the host shuts down, and the next host
// start reconciles. Live terminals are not touched.
func (m *Manager) Close() {
	m.reconcileMu.Lock()
	m.closed = true
	m.reconcileMu.Unlock()
}

// killStored ends the guest PTY of a stored session that is not live (its
// terminal was detached, the host restarted, or it is recorded as exited). It does not wake the VM;
// when the guest cannot be reached, Reconcile ends the PTY later, because
// no session refers to it any more.
func (m *Manager) killStored(ctx context.Context, tenantID, harness, nativeID string) {
	killer, ok := m.opts.Runner.(PTYKiller)
	if !ok {
		return
	}
	metas, err := m.opts.Store.ListSessionMeta(ctx, tenantID, harness)
	if err != nil {
		return
	}
	for _, sm := range metas {
		if sm.SessionID != nativeID {
			continue
		}
		// Any status: a session recorded as exited can still have a
		// running process (a record of an older host version).
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		if x.PTYID == "" {
			return
		}
		if err := killer.KillPTY(ctx, tenantID, x.PTYID); err != nil && !errors.Is(err, ErrNotFound) {
			m.log.Info("end the guest terminal of a deleted session later", "pty", x.PTYID, "err", err)
		}
		return
	}
}
