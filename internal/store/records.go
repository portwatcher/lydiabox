package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// HarnessBinding is the default model bound to one harness.
type HarnessBinding struct {
	Harness   string    `json:"harness"`
	Model     string    `json:"model"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SetHarnessBinding binds model to harness for the tenant.
func (s *Store) SetHarnessBinding(ctx context.Context, tenantID, harness, model string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if harness == "" || model == "" {
		return errors.New("store: harness and model are required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO harness_bindings (tenant_id, harness, model, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (tenant_id, harness) DO UPDATE SET model = excluded.model, updated_at = excluded.updated_at`,
		tenantID, harness, model, s.timestamp())
	if err != nil {
		return fmt.Errorf("store: set harness binding: %w", err)
	}
	return nil
}

// GetHarnessBinding returns the binding of one harness or ErrNotFound.
func (s *Store) GetHarnessBinding(ctx context.Context, tenantID, harness string) (*HarnessBinding, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	var (
		b  HarnessBinding
		up int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT harness, model, updated_at FROM harness_bindings WHERE tenant_id = ? AND harness = ?`,
		tenantID, harness).Scan(&b.Harness, &b.Model, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get harness binding: %w", err)
	}
	b.UpdatedAt = fromMillis(up)
	return &b, nil
}

// ListHarnessBindings returns every binding of the tenant ordered by harness.
func (s *Store) ListHarnessBindings(ctx context.Context, tenantID string) ([]HarnessBinding, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT harness, model, updated_at FROM harness_bindings WHERE tenant_id = ? ORDER BY harness`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list harness bindings: %w", err)
	}
	defer rows.Close()
	out := []HarnessBinding{}
	for rows.Next() {
		var (
			b  HarnessBinding
			up int64
		)
		if err := rows.Scan(&b.Harness, &b.Model, &up); err != nil {
			return nil, fmt.Errorf("store: list harness bindings: %w", err)
		}
		b.UpdatedAt = fromMillis(up)
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteHarnessBinding removes a binding or returns ErrNotFound.
func (s *Store) DeleteHarnessBinding(ctx context.Context, tenantID, harness string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM harness_bindings WHERE tenant_id = ? AND harness = ?`, tenantID, harness)
	if err != nil {
		return fmt.Errorf("store: delete harness binding: %w", err)
	}
	return requireAffected(res)
}

// AuditEntry is one row of the audit log.
type AuditEntry struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"createdAt"`
}

// AppendAudit records an audit event. kind is a dotted identifier such as
// "connector.token.issued"; detail is free text or JSON and must not contain
// secrets.
func (s *Store) AppendAudit(ctx context.Context, tenantID, kind, detail string) (int64, error) {
	if err := checkTenant(tenantID); err != nil {
		return 0, err
	}
	if kind == "" {
		return 0, errors.New("store: audit kind is required")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (tenant_id, kind, detail, created_at) VALUES (?, ?, ?, ?)`,
		tenantID, kind, detail, s.timestamp())
	if err != nil {
		return 0, fmt.Errorf("store: append audit: %w", err)
	}
	return res.LastInsertId()
}

// AuditQuery filters ListAudit. KindPrefix matches kinds that start with the
// value; BeforeID pages backwards; Limit defaults to 100 and is capped at 1000.
type AuditQuery struct {
	KindPrefix string
	BeforeID   int64
	Limit      int
}

// ListAudit returns entries newest first.
func (s *Store) ListAudit(ctx context.Context, tenantID string, q AuditQuery) ([]AuditEntry, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, detail, created_at FROM audit_log
WHERE tenant_id = ? AND substr(kind, 1, length(?)) = ? AND (? = 0 OR id < ?)
ORDER BY id DESC LIMIT ?`, tenantID, q.KindPrefix, q.KindPrefix, q.BeforeID, q.BeforeID, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var (
			e  AuditEntry
			ts int64
		)
		if err := rows.Scan(&e.ID, &e.Kind, &e.Detail, &ts); err != nil {
			return nil, fmt.Errorf("store: list audit: %w", err)
		}
		e.CreatedAt = fromMillis(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SessionMeta is host-side metadata about one harness session.
type SessionMeta struct {
	Harness      string          `json:"harness"`
	SessionID    string          `json:"sessionId"`
	WorkspaceDir string          `json:"workspaceDir"`
	Title        string          `json:"title"`
	Model        string          `json:"model"`
	Status       string          `json:"status"`
	Extra        json.RawMessage `json:"extra"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

// UpsertSessionMeta inserts or updates session metadata; CreatedAt is kept on update.
func (s *Store) UpsertSessionMeta(ctx context.Context, tenantID string, m SessionMeta) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if m.Harness == "" || m.SessionID == "" {
		return errors.New("store: harness and session ID are required")
	}
	extra := string(m.Extra)
	if extra == "" {
		extra = "{}"
	}
	if !json.Valid([]byte(extra)) {
		return errors.New("store: session extra must be valid JSON")
	}
	now := s.timestamp()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions_meta (tenant_id, harness, session_id, workspace_dir, title, model, status, extra_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, harness, session_id) DO UPDATE SET
	workspace_dir = excluded.workspace_dir, title = excluded.title, model = excluded.model,
	status = excluded.status, extra_json = excluded.extra_json, updated_at = excluded.updated_at`,
		tenantID, m.Harness, m.SessionID, m.WorkspaceDir, m.Title, m.Model, m.Status, extra, now, now)
	if err != nil {
		return fmt.Errorf("store: upsert session meta: %w", err)
	}
	return nil
}

// ListSessionMeta returns sessions newest first; an empty harness lists all.
func (s *Store) ListSessionMeta(ctx context.Context, tenantID, harness string) ([]SessionMeta, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT harness, session_id, workspace_dir, title, model, status, extra_json, created_at, updated_at
FROM sessions_meta WHERE tenant_id = ? AND (? = '' OR harness = ?) ORDER BY updated_at DESC, session_id`,
		tenantID, harness, harness)
	if err != nil {
		return nil, fmt.Errorf("store: list session meta: %w", err)
	}
	defer rows.Close()
	out := []SessionMeta{}
	for rows.Next() {
		var (
			m           SessionMeta
			extra       string
			created, up int64
		)
		if err := rows.Scan(&m.Harness, &m.SessionID, &m.WorkspaceDir, &m.Title, &m.Model, &m.Status, &extra, &created, &up); err != nil {
			return nil, fmt.Errorf("store: list session meta: %w", err)
		}
		m.Extra = json.RawMessage(extra)
		m.CreatedAt, m.UpdatedAt = fromMillis(created), fromMillis(up)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteSessionMeta removes session metadata or returns ErrNotFound.
func (s *Store) DeleteSessionMeta(ctx context.Context, tenantID, harness, sessionID string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions_meta WHERE tenant_id = ? AND harness = ? AND session_id = ?`,
		tenantID, harness, sessionID)
	if err != nil {
		return fmt.Errorf("store: delete session meta: %w", err)
	}
	return requireAffected(res)
}
