package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DiscoveryCacheEntry is the cached discovery result of one harness.
type DiscoveryCacheEntry struct {
	Harness   string
	Payload   json.RawMessage
	UpdatedAt time.Time
}

// PutDiscoveryCache stores (or replaces) the discovery result of a harness.
func (s *Store) PutDiscoveryCache(ctx context.Context, tenantID, harness string, payload json.RawMessage) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session_discovery_cache (tenant_id, harness, payload_json, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (tenant_id, harness) DO UPDATE SET payload_json = excluded.payload_json, updated_at = excluded.updated_at`,
		tenantID, harness, string(payload), s.timestamp())
	if err != nil {
		return fmt.Errorf("store: put discovery cache: %w", err)
	}
	return nil
}

// ListDiscoveryCache returns the cached discovery results of a tenant.
func (s *Store) ListDiscoveryCache(ctx context.Context, tenantID string) ([]DiscoveryCacheEntry, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT harness, payload_json, updated_at FROM session_discovery_cache WHERE tenant_id = ? ORDER BY harness`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list discovery cache: %w", err)
	}
	defer rows.Close()
	var out []DiscoveryCacheEntry
	for rows.Next() {
		var (
			e       DiscoveryCacheEntry
			payload string
			updated int64
		)
		if err := rows.Scan(&e.Harness, &payload, &updated); err != nil {
			return nil, fmt.Errorf("store: list discovery cache: %w", err)
		}
		e.Payload, e.UpdatedAt = json.RawMessage(payload), fromMillis(updated)
		out = append(out, e)
	}
	return out, rows.Err()
}
