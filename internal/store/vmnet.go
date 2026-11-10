package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func init() {
	registerMigration(Migration{ID: "0100_vm_networks", SQL: vmNetworksSchema})
}

// vmNetworksSchema assigns each tenant a stable guest subnet index (the
// third octet of 172.30.<n>.0/24 for the chv driver).
const vmNetworksSchema = `
CREATE TABLE vm_networks (
	tenant_id  TEXT    NOT NULL PRIMARY KEY,
	subnet     INTEGER NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);
`

// DefaultTenantSubnet is the subnet index of the "default" tenant.
const DefaultTenantSubnet = 0

// MaxTenantSubnet is the highest subnet index (172.30.254.0/24).
const MaxTenantSubnet = 254

// ErrNoFreeSubnet is returned when every subnet index is assigned.
var ErrNoFreeSubnet = errors.New("store: no free guest subnet")

// TenantSubnet returns the guest subnet index of a tenant and assigns the
// lowest free one on first use. The "default" tenant always has index 0;
// other tenants get 1 to 254. The index never changes once assigned.
func (s *Store) TenantSubnet(ctx context.Context, tenantID string) (int, error) {
	if tenantID == "" {
		return 0, errors.New("store: tenant ID is required")
	}
	if tenantID == "default" {
		return DefaultTenantSubnet, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	err = tx.QueryRowContext(ctx, `SELECT subnet FROM vm_networks WHERE tenant_id = ?`, tenantID).Scan(&n)
	if err == nil {
		return n, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: read subnet: %w", err)
	}
	// Lowest unused index from 1: 1 when none is used, else the first gap.
	err = tx.QueryRowContext(ctx, `
SELECT COALESCE(MIN(n), 0) FROM (
	SELECT 1 AS n WHERE NOT EXISTS (SELECT 1 FROM vm_networks WHERE subnet = 1)
	UNION ALL
	SELECT subnet + 1 FROM vm_networks
	WHERE subnet + 1 <= ? AND subnet + 1 NOT IN (SELECT subnet FROM vm_networks)
)`, MaxTenantSubnet).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: find subnet: %w", err)
	}
	if n == 0 {
		return 0, ErrNoFreeSubnet
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vm_networks (tenant_id, subnet, created_at) VALUES (?, ?, ?)`,
		tenantID, n, s.now().Unix()); err != nil {
		return 0, fmt.Errorf("store: assign subnet: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: assign subnet: %w", err)
	}
	return n, nil
}
