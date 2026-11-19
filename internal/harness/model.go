package harness

import (
	"context"
	"fmt"
	"strings"
)

// GatewayProvider is the provider name of the LydiaBox model gateway in
// the harness configs (wire.ProviderID): opencode's provider and dsh's
// pi-ai route. A model of this provider is a gateway model ID.
const GatewayProvider = "lydiabox"

// GatewayModel returns a model as the session list shows it: a gateway
// model ID without the "lydiabox/" provider prefix that opencode and dsh
// record ("lydiabox/codex/gpt-6.1-sol" becomes "codex/gpt-6.1-sol").
// Other models are returned as they are.
func GatewayModel(model string) string {
	if m, ok := strings.CutPrefix(model, GatewayProvider+"/"); ok && m != "" {
		return m
	}
	return model
}

// ModelMemory is implemented by wirings that keep the last model of each
// harness (*magpie.Bindings). A session start with a model makes it the
// default of its harness: the next new session preselects it, and the
// harness config uses it when the harness starts without a model.
type ModelMemory interface {
	RememberModel(ctx context.Context, tenantID, harness, model string) error
}

// wire points the harness config at the gateway before a start, with the
// model of the session when it has one.
func (m *Manager) wire(ctx context.Context, ls *liveSession) {
	if ls.harness == Shell || m.opts.Wiring == nil {
		return
	}
	if mem, ok := m.opts.Wiring.(ModelMemory); ok && ls.model != "" {
		err := mem.RememberModel(ctx, ls.tenantID, ls.harness, ls.model)
		if err == nil {
			return
		}
		m.log.Info("harness model not remembered; wiring with the stored model", "harness", ls.harness, "model", ls.model, "err", err)
	}
	if err := m.opts.Wiring.EnsureWired(ctx, ls.tenantID, ls.harness); err != nil {
		m.log.Warn("harness config wiring failed; starting with the existing config", "harness", ls.harness, "err", err)
	}
}

// ModelRequest changes the model of a live session.
type ModelRequest struct {
	Model string `json:"model"`
	Cols  uint16 `json:"cols"`
	Rows  uint16 `json:"rows"`
}

// SetModel changes the model of a live session: the harness process ends
// and the same native session resumes with the new model, so the
// conversation stays. The harness of a session cannot change.
func (m *Manager) SetModel(ctx context.Context, tenantID, id string, req ModelRequest) (Session, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" || strings.ContainsAny(model, " \t\r\n\x00") {
		return Session{}, fmt.Errorf("%w: model must be a model ID", ErrBadRequest)
	}
	ls := m.lookupLive(tenantID, id)
	if ls == nil {
		if _, err := m.Get(ctx, tenantID, id); err != nil {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("%w: session %s is not running; resume it with the model instead", ErrConflict, id)
	}
	switch {
	case ls.harness == Shell:
		return Session{}, fmt.Errorf("%w: a shell has no model", ErrBadRequest)
	case ls.provisional:
		return Session{}, fmt.Errorf("%w: the harness has not saved this session yet; send a first message, then change the model", ErrConflict)
	case ls.model == model:
		return m.sessionOf(ls), nil
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
	return m.Create(ctx, tenantID, CreateRequest{Harness: ls.harness, WorkspaceDir: ls.workspace, Model: model,
		ResumeNativeID: ls.nativeID, Title: ls.title, Engine: ls.engine, Cols: cols, Rows: rows})
}
