package harness

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// reattachTimeout bounds one attach of a watched terminal.
const reattachTimeout = 10 * time.Second

// vmServing reports whether the machine of a tenant runs and no pause or
// stop of it is in progress, from the waker (vmm.Controller.Serving) and
// the discovery (GuestDiscovery.Serving) when they report it.
func (m *Manager) vmServing(tenantID string) bool {
	if s, ok := m.opts.Waker.(servingReporter); ok && !s.Serving(tenantID) {
		return false
	}
	return m.serving(tenantID)
}

// reattachWatched attaches again the guest terminal of a watched session
// that is stored as running but is not live: a guest link loss detached it
// while the VM kept running, so no Reconcile follows. It uses the no-wake
// attach of Reconcile and runs only while the machine serves, never during
// a pause or stop. A terminal that the guest no longer has makes the
// session exited (attachStored). It reports whether the session is live
// after the call.
func (m *Manager) reattachWatched(ctx context.Context, tenantID, id string) bool {
	ra, ok := m.opts.Runner.(RunningAttacher)
	if !ok || !m.vmServing(tenantID) {
		return false
	}
	// Reconcile attaches the same terminals: one of the two at a time.
	if !m.reconcileMu.TryLock() {
		return false
	}
	defer m.reconcileMu.Unlock()
	if m.closed {
		return false
	}
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
		if sm.Status != StatusRunning || x.PTYID == "" {
			return false
		}
		if m.lookupLive(tenantID, id) != nil {
			return true
		}
		actx, cancel := context.WithTimeout(ctx, reattachTimeout)
		defer cancel()
		_, err := m.attachStored(actx, tenantID, sm, x, ra.AttachRunningPTY)
		if err != nil && !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
			m.log.Debug("attach a watched guest terminal again", "session", id, "err", err)
		}
		return err == nil
	}
	return false
}
