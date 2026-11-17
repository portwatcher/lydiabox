package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func init() {
	registerMigration(Migration{ID: "0120_vm_holds", SQL: vmHoldsSchema})
}

// vmHoldsSchema records the VMs that the user stopped: a row is present
// from a user stop until an explicit start (docs/api.md section 7), so a
// new host process does not wake the VM on demand.
const vmHoldsSchema = `
CREATE TABLE vm_holds (
	tenant_id TEXT    NOT NULL PRIMARY KEY,
	held_at   INTEGER NOT NULL
);
`

// VMHold reports whether the user stopped the VM of tenantID, and since
// when.
func (s *Store) VMHold(ctx context.Context, tenantID string) (bool, time.Time, error) {
	if err := checkTenant(tenantID); err != nil {
		return false, time.Time{}, err
	}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT held_at FROM vm_holds WHERE tenant_id = ?`, tenantID).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, fmt.Errorf("store: get VM hold: %w", err)
	}
	return true, fromMillis(at), nil
}

// SetVMHold sets (held) or clears the user-stop hold of tenantID. Each
// change is one SQLite statement, so it is atomic.
func (s *Store) SetVMHold(ctx context.Context, tenantID string, held bool) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	var err error
	if held {
		_, err = s.db.ExecContext(ctx, `INSERT INTO vm_holds (tenant_id, held_at) VALUES (?, ?)
ON CONFLICT (tenant_id) DO NOTHING`, tenantID, s.timestamp())
	} else {
		_, err = s.db.ExecContext(ctx, `DELETE FROM vm_holds WHERE tenant_id = ?`, tenantID)
	}
	if err != nil {
		return fmt.Errorf("store: set VM hold: %w", err)
	}
	return nil
}
