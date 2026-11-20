package harness

import (
	"context"
	"encoding/json"
	"strings"
)

// EventWaker wakes the machine of a tenant when a client opens a session
// that has no transcript to read (a provisional session). It uses the wake
// of the event read: a machine that the user stopped is not started
// (implemented by *GuestDiscovery).
type EventWaker interface {
	WakeForEvents(ctx context.Context, tenantID string) error
}

// aliasTarget returns the API ID that id has now: a provisional session
// that adopted its native ID has an alias to the new ID.
func (m *Manager) aliasTarget(tenantID, id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if to, ok := m.aliases[liveKey(tenantID, id)]; ok {
		return strings.TrimPrefix(to, liveKey(tenantID, ""))
	}
	return id
}

// storedProvisional reports whether the stored record of the session id is
// provisional: the harness did not write a transcript yet, so the session
// has no native ID and no events to read.
func (m *Manager) storedProvisional(ctx context.Context, tenantID, id string) bool {
	name, nativeID, err := SplitSessionID(id)
	if err != nil {
		return false
	}
	metas, err := m.opts.Store.ListSessionMeta(ctx, tenantID, name)
	if err != nil {
		return false
	}
	for _, sm := range metas {
		if sm.SessionID != nativeID {
			continue
		}
		var x extra
		_ = json.Unmarshal(sm.Extra, &x)
		return x.Provisional
	}
	return false
}

// isProvisional reports whether the session id (live or stored) has no
// native ID yet.
func (m *Manager) isProvisional(ctx context.Context, tenantID, id string) bool {
	if lv, live := m.liveInfo(tenantID, id); live {
		return lv.provisional
	}
	return m.storedProvisional(ctx, tenantID, id)
}

// wakeOnce asks once per hub, in the background, to wake the machine for a
// session without a transcript. The clients do not wait for it: the hub
// reports the session as synced with no events at once, and as live when
// Reconcile attaches its terminal again.
func (h *eventHub) wakeOnce(ctx context.Context) {
	w, ok := h.m.opts.Discovery.(EventWaker)
	if !ok {
		return
	}
	h.mu.Lock()
	if h.woke {
		h.mu.Unlock()
		return
	}
	h.woke = true
	h.mu.Unlock()
	go func() {
		if err := w.WakeForEvents(ctx, h.tenantID); err != nil && ctx.Err() == nil {
			h.m.log.Info("wake the machine for a session without a transcript", "session", h.id, "err", err)
		}
	}()
}
