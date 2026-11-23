package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func magpieSecretAAD(tenantID, name string) []byte {
	return []byte("magpie_secret\x00" + tenantID + "\x00" + name)
}

// PutMagpieSecret stores (or replaces) the encrypted secret called name.
func (s *Store) PutMagpieSecret(ctx context.Context, tenantID, name, secret string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if name == "" || secret == "" {
		return errors.New("store: magpie secret name and value are required")
	}
	enc, err := s.cipher.EncryptString(secret, magpieSecretAAD(tenantID, name))
	if err != nil {
		return err
	}
	now := s.timestamp()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO magpie_secrets (tenant_id, name, secret_enc, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, name) DO UPDATE SET secret_enc = excluded.secret_enc, updated_at = excluded.updated_at`,
		tenantID, name, enc, now, now)
	if err != nil {
		return fmt.Errorf("store: put magpie secret: %w", err)
	}
	return nil
}

// GetMagpieSecret returns the decrypted secret called name or ErrNotFound.
func (s *Store) GetMagpieSecret(ctx context.Context, tenantID, name string) (string, error) {
	if err := checkTenant(tenantID); err != nil {
		return "", err
	}
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT secret_enc FROM magpie_secrets WHERE tenant_id = ? AND name = ?`,
		tenantID, name).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: get magpie secret: %w", err)
	}
	v, err := s.cipher.DecryptString(enc, magpieSecretAAD(tenantID, name))
	if err != nil {
		return "", fmt.Errorf("store: decrypt magpie secret %s: %w", name, err)
	}
	return v, nil
}

// DeleteMagpieSecret removes the secret called name or returns ErrNotFound.
func (s *Store) DeleteMagpieSecret(ctx context.Context, tenantID, name string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM magpie_secrets WHERE tenant_id = ? AND name = ?`, tenantID, name)
	if err != nil {
		return fmt.Errorf("store: delete magpie secret: %w", err)
	}
	return requireAffected(res)
}

// DeleteMagpieSecrets removes every magpie secret of a tenant and returns
// the number of rows removed (guest-native mode keeps none).
func (s *Store) DeleteMagpieSecrets(ctx context.Context, tenantID string) (int64, error) {
	if err := checkTenant(tenantID); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM magpie_secrets WHERE tenant_id = ?`, tenantID)
	if err != nil {
		return 0, fmt.Errorf("store: delete magpie secrets: %w", err)
	}
	return res.RowsAffected()
}
