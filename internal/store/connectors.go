package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConnectorAccount is one stored connector credential. AccessToken and
// RefreshToken are plaintext in memory and AES-256-GCM encrypted at rest.
type ConnectorAccount struct {
	Provider     string    `json:"provider"`
	AccountLabel string    `json:"accountLabel"`
	AuthKind     string    `json:"authKind"` // "oauth2" or "pat"
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	TokenType    string    `json:"tokenType"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Scopes       []string  `json:"scopes"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func tokenAAD(tenantID, provider, label, column string) []byte {
	return []byte("lydiabox/connector_accounts/v1\x00" + tenantID + "\x00" + provider + "\x00" + label + "\x00" + column)
}

// UpsertConnectorAccount inserts or replaces a connector credential. The
// tokens are encrypted with associated data bound to the tenant, provider,
// label, and column.
func (s *Store) UpsertConnectorAccount(ctx context.Context, tenantID string, a ConnectorAccount) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if a.Provider == "" || a.AccountLabel == "" {
		return errors.New("store: connector provider and account label are required")
	}
	if a.AuthKind == "" {
		a.AuthKind = "oauth2"
	}
	access, err := s.cipher.EncryptString(a.AccessToken, tokenAAD(tenantID, a.Provider, a.AccountLabel, "access"))
	if err != nil {
		return err
	}
	refresh, err := s.cipher.EncryptString(a.RefreshToken, tokenAAD(tenantID, a.Provider, a.AccountLabel, "refresh"))
	if err != nil {
		return err
	}
	now := s.timestamp()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO connector_accounts (tenant_id, provider, account_label, auth_kind, access_token_enc,
	refresh_token_enc, token_type, expires_at, scopes, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, provider, account_label) DO UPDATE SET
	auth_kind = excluded.auth_kind,
	access_token_enc = excluded.access_token_enc,
	refresh_token_enc = excluded.refresh_token_enc,
	token_type = excluded.token_type,
	expires_at = excluded.expires_at,
	scopes = excluded.scopes,
	updated_at = excluded.updated_at`,
		tenantID, a.Provider, a.AccountLabel, a.AuthKind, access, refresh, a.TokenType,
		toMillis(a.ExpiresAt), strings.Join(a.Scopes, " "), now, now)
	if err != nil {
		return fmt.Errorf("store: upsert connector account: %w", err)
	}
	return nil
}

// GetConnectorAccount returns one credential with decrypted tokens.
func (s *Store) GetConnectorAccount(ctx context.Context, tenantID, provider, label string) (*ConnectorAccount, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `
SELECT provider, account_label, auth_kind, access_token_enc, refresh_token_enc, token_type,
	expires_at, scopes, created_at, updated_at
FROM connector_accounts WHERE tenant_id = ? AND provider = ? AND account_label = ?`, tenantID, provider, label)
	var (
		a                ConnectorAccount
		access, refresh  []byte
		exp, created, up int64
		scopes           string
	)
	err := row.Scan(&a.Provider, &a.AccountLabel, &a.AuthKind, &access, &refresh, &a.TokenType, &exp, &scopes, &created, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get connector account: %w", err)
	}
	if a.AccessToken, err = s.cipher.DecryptString(access, tokenAAD(tenantID, provider, label, "access")); err != nil {
		return nil, err
	}
	if a.RefreshToken, err = s.cipher.DecryptString(refresh, tokenAAD(tenantID, provider, label, "refresh")); err != nil {
		return nil, err
	}
	a.ExpiresAt, a.CreatedAt, a.UpdatedAt = fromMillis(exp), fromMillis(created), fromMillis(up)
	a.Scopes = splitScopes(scopes)
	return &a, nil
}

// ListConnectorAccounts returns credential metadata without tokens. An empty
// provider lists every provider.
func (s *Store) ListConnectorAccounts(ctx context.Context, tenantID, provider string) ([]ConnectorAccount, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT provider, account_label, auth_kind, token_type, expires_at, scopes, created_at, updated_at
FROM connector_accounts WHERE tenant_id = ? AND (? = '' OR provider = ?)
ORDER BY provider, account_label`, tenantID, provider, provider)
	if err != nil {
		return nil, fmt.Errorf("store: list connector accounts: %w", err)
	}
	defer rows.Close()
	out := []ConnectorAccount{}
	for rows.Next() {
		var (
			a                ConnectorAccount
			exp, created, up int64
			scopes           string
		)
		if err := rows.Scan(&a.Provider, &a.AccountLabel, &a.AuthKind, &a.TokenType, &exp, &scopes, &created, &up); err != nil {
			return nil, fmt.Errorf("store: list connector accounts: %w", err)
		}
		a.ExpiresAt, a.CreatedAt, a.UpdatedAt = fromMillis(exp), fromMillis(created), fromMillis(up)
		a.Scopes = splitScopes(scopes)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteConnectorAccount removes a credential. It returns ErrNotFound when no
// row matched.
func (s *Store) DeleteConnectorAccount(ctx context.Context, tenantID, provider, label string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM connector_accounts WHERE tenant_id = ? AND provider = ? AND account_label = ?`,
		tenantID, provider, label)
	if err != nil {
		return fmt.Errorf("store: delete connector account: %w", err)
	}
	return requireAffected(res)
}

func splitScopes(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Fields(s)
}

func requireAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
