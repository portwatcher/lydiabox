package store

import (
	"context"
	"fmt"
	"time"
)

// DismissedSession is a discovered session that the user deleted while its
// transcript stays.
type DismissedSession struct {
	Harness     string
	SessionID   string
	DismissedAt time.Time
}

// DismissSession records (or renews) the dismissal of a discovered session
// at the current time.
func (s *Store) DismissSession(ctx context.Context, tenantID, harness, sessionID string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_dismissed (tenant_id, harness, session_id, dismissed_at) VALUES (?, ?, ?, ?)
ON CONFLICT (tenant_id, harness, session_id) DO UPDATE SET dismissed_at = excluded.dismissed_at`,
		tenantID, harness, sessionID, s.timestamp())
	if err != nil {
		return fmt.Errorf("store: dismiss session: %w", err)
	}
	return nil
}

// UndismissSession removes a dismissal; an absent one is no error.
func (s *Store) UndismissSession(ctx context.Context, tenantID, harness, sessionID string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_dismissed WHERE tenant_id = ? AND harness = ? AND session_id = ?`,
		tenantID, harness, sessionID); err != nil {
		return fmt.Errorf("store: undismiss session: %w", err)
	}
	return nil
}

// ListDismissedSessions returns the dismissed sessions of a tenant.
func (s *Store) ListDismissedSessions(ctx context.Context, tenantID string) ([]DismissedSession, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT harness, session_id, dismissed_at FROM session_dismissed WHERE tenant_id = ? ORDER BY harness, session_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list dismissed sessions: %w", err)
	}
	defer rows.Close()
	var out []DismissedSession
	for rows.Next() {
		var (
			d  DismissedSession
			at int64
		)
		if err := rows.Scan(&d.Harness, &d.SessionID, &at); err != nil {
			return nil, fmt.Errorf("store: list dismissed sessions: %w", err)
		}
		d.DismissedAt = fromMillis(at)
		out = append(out, d)
	}
	return out, rows.Err()
}
