package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RemoteAccess is the stored remote access setup of one provider.
type RemoteAccess struct {
	Provider string
	// State is provider-defined JSON without secrets.
	State []byte
	// Secrets is provider-defined JSON; it is encrypted at rest.
	Secrets   []byte
	UpdatedAt time.Time
}

func remoteAccessAAD(tenantID, provider string) []byte {
	return []byte("remote_access\x00" + tenantID + "\x00" + provider)
}

// PutRemoteAccess stores (or replaces) the setup of ra.Provider.
func (s *Store) PutRemoteAccess(ctx context.Context, tenantID string, ra RemoteAccess) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if ra.Provider == "" || len(ra.State) == 0 {
		return errors.New("store: remote access provider and state are required")
	}
	var enc []byte
	if len(ra.Secrets) > 0 {
		var err error
		if enc, err = s.cipher.Encrypt(ra.Secrets, remoteAccessAAD(tenantID, ra.Provider)); err != nil {
			return err
		}
	}
	now := s.timestamp()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO remote_access (tenant_id, provider, state_json, secrets_enc, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, provider) DO UPDATE SET state_json = excluded.state_json,
	secrets_enc = excluded.secrets_enc, updated_at = excluded.updated_at`,
		tenantID, ra.Provider, string(ra.State), enc, now, now)
	if err != nil {
		return fmt.Errorf("store: put remote access: %w", err)
	}
	return nil
}

// GetRemoteAccess returns the setup of provider (secrets decrypted) or
// ErrNotFound.
func (s *Store) GetRemoteAccess(ctx context.Context, tenantID, provider string) (RemoteAccess, error) {
	if err := checkTenant(tenantID); err != nil {
		return RemoteAccess{}, err
	}
	var (
		state   string
		enc     []byte
		updated int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT state_json, secrets_enc, updated_at FROM remote_access WHERE tenant_id = ? AND provider = ?`,
		tenantID, provider).Scan(&state, &enc, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteAccess{}, ErrNotFound
	}
	if err != nil {
		return RemoteAccess{}, fmt.Errorf("store: get remote access: %w", err)
	}
	ra := RemoteAccess{Provider: provider, State: []byte(state), UpdatedAt: fromMillis(updated)}
	if len(enc) > 0 {
		if ra.Secrets, err = s.cipher.Decrypt(enc, remoteAccessAAD(tenantID, provider)); err != nil {
			return RemoteAccess{}, fmt.Errorf("store: decrypt remote access secrets: %w", err)
		}
	}
	return ra, nil
}

// DeleteRemoteAccess removes the setup of provider or returns ErrNotFound.
func (s *Store) DeleteRemoteAccess(ctx context.Context, tenantID, provider string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM remote_access WHERE tenant_id = ? AND provider = ?`, tenantID, provider)
	if err != nil {
		return fmt.Errorf("store: delete remote access: %w", err)
	}
	return requireAffected(res)
}
