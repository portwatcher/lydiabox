package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func init() {
	registerMigration(Migration{ID: "0110_vm_machines", SQL: vmMachinesSchema})
}

// vmMachinesSchema records the remote machine of each tenant for a remote
// VM driver (for example instabox): the non-secret state as JSON (machine
// ID, create idempotency key, link URL), and the secrets (link secret,
// private port token) AES-256-GCM encrypted with the master key.
const vmMachinesSchema = `
CREATE TABLE vm_machines (
	tenant_id   TEXT    NOT NULL,
	driver      TEXT    NOT NULL,
	state_json  TEXT    NOT NULL,
	secrets_enc BLOB,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, driver)
);
`

// VMMachine is the stored record of a tenant's remote machine.
type VMMachine struct {
	Driver string
	// State is driver-defined JSON without secrets.
	State []byte
	// Secrets is driver-defined JSON; it is encrypted at rest.
	Secrets   []byte
	UpdatedAt time.Time
}

func vmMachineAAD(tenantID, driver string) []byte {
	return []byte("vm_machines\x00" + tenantID + "\x00" + driver)
}

// PutVMMachine stores (or replaces) the machine record of m.Driver.
func (s *Store) PutVMMachine(ctx context.Context, tenantID string, m VMMachine) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if m.Driver == "" || len(m.State) == 0 {
		return errors.New("store: VM machine driver and state are required")
	}
	var enc []byte
	if len(m.Secrets) > 0 {
		var err error
		if enc, err = s.cipher.Encrypt(m.Secrets, vmMachineAAD(tenantID, m.Driver)); err != nil {
			return err
		}
	}
	now := s.timestamp()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO vm_machines (tenant_id, driver, state_json, secrets_enc, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, driver) DO UPDATE SET state_json = excluded.state_json,
	secrets_enc = excluded.secrets_enc, updated_at = excluded.updated_at`,
		tenantID, m.Driver, string(m.State), enc, now, now)
	if err != nil {
		return fmt.Errorf("store: put VM machine: %w", err)
	}
	return nil
}

// GetVMMachine returns the machine record of driver (secrets decrypted) or
// ErrNotFound.
func (s *Store) GetVMMachine(ctx context.Context, tenantID, driver string) (VMMachine, error) {
	if err := checkTenant(tenantID); err != nil {
		return VMMachine{}, err
	}
	var (
		state   string
		enc     []byte
		updated int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT state_json, secrets_enc, updated_at FROM vm_machines WHERE tenant_id = ? AND driver = ?`,
		tenantID, driver).Scan(&state, &enc, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return VMMachine{}, ErrNotFound
	}
	if err != nil {
		return VMMachine{}, fmt.Errorf("store: get VM machine: %w", err)
	}
	m := VMMachine{Driver: driver, State: []byte(state), UpdatedAt: fromMillis(updated)}
	if len(enc) > 0 {
		if m.Secrets, err = s.cipher.Decrypt(enc, vmMachineAAD(tenantID, driver)); err != nil {
			return VMMachine{}, fmt.Errorf("store: decrypt VM machine secrets: %w", err)
		}
	}
	return m, nil
}

// DeleteVMMachine removes the machine record of driver or returns
// ErrNotFound.
func (s *Store) DeleteVMMachine(ctx context.Context, tenantID, driver string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM vm_machines WHERE tenant_id = ? AND driver = ?`, tenantID, driver)
	if err != nil {
		return fmt.Errorf("store: delete VM machine: %w", err)
	}
	return requireAffected(res)
}
