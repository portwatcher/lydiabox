package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// maxBridgeLine bounds one line of bridge output that the hub buffers.
const maxBridgeLine = 16 << 20

// followACP streams the events of a live ACP session from its bridge until
// the terminal ends or ctx ends. The room replay is dropped: the hub asks
// the bridge to send every event again (sync).
func (h *eventHub) followACP(ctx context.Context, lv liveView) {
	sub, _, _, ok := lv.room.Subscribe()
	if !ok {
		return
	}
	defer lv.room.Unsubscribe(sub)
	h.mu.Lock()
	h.clearLocked()
	h.source = "acp"
	h.mu.Unlock()
	status := EventStatus{SessionID: lv.current, State: StateWorking, Live: true, Engine: EngineACP}
	h.publish(true, nil, status)
	_ = lv.room.Write([]byte(`{"type":"sync"}` + "\n"))
	var buf []byte
	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-sub.C:
			if !ok {
				if status.State == StateOffline {
					h.ended = status
				}
				return
			}
			if f.Data == nil {
				continue
			}
			buf = append(buf, f.Data...)
			for {
				i := bytes.IndexByte(buf, '\n')
				if i < 0 {
					break
				}
				status = h.applyBridge(buf[:i], lv, status)
				buf = buf[i+1:]
			}
			if len(buf) > maxBridgeLine {
				buf = nil
			}
		}
	}
}

// applyBridge applies one line of bridge output and returns the status.
func (h *eventHub) applyBridge(line []byte, lv liveView, status EventStatus) EventStatus {
	var msg BridgeMessage
	if json.Unmarshal(bytes.TrimSpace(line), &msg) != nil {
		return status // the echo of an early command, or a partial line
	}
	switch msg.Type {
	case BridgeReset:
		h.mu.Lock()
		h.clearLocked()
		h.source = "acp"
		h.mu.Unlock()
		status.Synced = true
		h.publish(true, nil, status)
	case BridgeEvent:
		if msg.Event == nil || msg.Event.Key == "" {
			return status
		}
		h.mu.Lock()
		changed := h.upsertLocked([]Event{*msg.Event})
		h.mu.Unlock()
		if len(changed) > 0 && (!lv.provisional || status.SessionID != lv.current) {
			// The copy is of the native session; a provisional one has
			// none until the bridge reports its native ID.
			h.markDirty(status.SessionID)
		}
		h.publish(false, changed, status)
	case BridgeStatus:
		if msg.Status == nil {
			return status
		}
		s := msg.Status
		if s.NativeID != "" && s.NativeID != lv.nativeID {
			h.m.adoptNative(h.tenantID, h.id, s.NativeID)
			status.SessionID = SessionID(lv.harness, s.NativeID)
		}
		status.Agent, status.Mode, status.Queued, status.Error = s.Agent, s.Mode, s.Queued, s.Error
		switch s.State {
		case AgentIdle:
			status.State = StateIdle
		case AgentAttention:
			status.State = StateAttention
		case AgentEnded:
			status.State = StateOffline
		default:
			status.State = StateWorking
		}
		h.publish(false, nil, status)
	}
	return status
}

// bridgeCommand sends a command to the bridge of a live ACP session.
func (m *Manager) bridgeCommand(ctx context.Context, tenantID, id string, c BridgeCommand) error {
	lv, ok := m.liveInfo(tenantID, id)
	if !ok || lv.engine != EngineACP || lv.room == nil {
		if _, err := m.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return fmt.Errorf("%w: session %s has no running chat agent", ErrConflict, id)
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := lv.room.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("%w: bridge input: %v", ErrUnavailable, err)
	}
	return nil
}

// AnswerPermission answers a permission request of the ACP agent of a
// session with one of its options.
func (m *Manager) AnswerPermission(ctx context.Context, tenantID, id, requestID, optionID string) error {
	if requestID == "" || optionID == "" {
		return fmt.Errorf("%w: requestId and optionId are required", ErrBadRequest)
	}
	return m.bridgeCommand(ctx, tenantID, id, BridgeCommand{Type: BridgePermission, RequestID: requestID, OptionID: optionID})
}
