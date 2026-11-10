package connector

import (
	"context"
	"encoding/json"
)

// Audit kinds (docs/api.md section 11).
const (
	KindConnected     = "connector.connected"
	KindDisconnected  = "connector.disconnected"
	KindTokenIssued   = "connector.token.issued"
	KindRefreshed     = "connector.token.refreshed"
	KindRefreshFailed = "connector.token.refresh_failed"
	KindMCPCall       = "connector.mcp.call"
)

// audit appends one entry. detail must never contain token material; the
// callers pass only provider, account, consumer, tool, and error codes.
// Audit failures are logged and do not fail the operation.
func (m *Manager) audit(ctx context.Context, tenantID, kind string, detail map[string]any) {
	b, err := json.Marshal(detail)
	if err != nil {
		m.log.Error("encode audit detail", "kind", kind, "err", err)
		return
	}
	if _, err := m.store.AppendAudit(context.WithoutCancel(ctx), tenantID, kind, string(b)); err != nil {
		m.log.Error("append audit entry", "kind", kind, "err", err)
	}
}

// MCPCall describes one connector MCP tool call for the audit log.
type MCPCall struct {
	Tool     string
	Provider string
	Account  string
	// Error is a short failure description, empty on success. It must not
	// contain token material (APIError messages satisfy this).
	Error string
}

// RecordMCPCall writes a connector.mcp.call audit entry.
func (m *Manager) RecordMCPCall(ctx context.Context, tenantID string, c MCPCall) {
	d := map[string]any{"provider": c.Provider, "tool": c.Tool, "consumer": "mcp", "ok": c.Error == ""}
	if c.Account != "" {
		d["account"] = c.Account
	}
	if c.Error != "" {
		d["error"] = truncate(c.Error, 300)
	}
	m.audit(ctx, tenantID, KindMCPCall, d)
}
