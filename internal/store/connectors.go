package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Connector account states.
const (
	AccountOK      = "ok"
	AccountExpired = "expired"
	AccountRevoked = "revoked"
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
	// Status is AccountOK, AccountExpired (refresh failed), or AccountRevoked.
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func tokenAAD(tenantID, provider, label, column string) []byte {
	return []byte("lydiabox/connector_accounts/v1\x00" + tenantID + "\x00" + provider + "\x00" + label + "\x00" + column)
}

// UpsertConnectorAccount inserts or replaces a connector credential. The
// tokens are encrypted with associated data bound to the tenant, provider,
// label, and column. An empty Status is stored as AccountOK.
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
	if a.Status == "" {
		a.Status = AccountOK
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
	refresh_token_enc, token_type, expires_at, scopes, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, provider, account_label) DO UPDATE SET
	auth_kind = excluded.auth_kind,
	access_token_enc = excluded.access_token_enc,
	refresh_token_enc = excluded.refresh_token_enc,
	token_type = excluded.token_type,
	expires_at = excluded.expires_at,
	scopes = excluded.scopes,
	status = excluded.status,
	updated_at = excluded.updated_at`,
		tenantID, a.Provider, a.AccountLabel, a.AuthKind, access, refresh, a.TokenType,
		toMillis(a.ExpiresAt), strings.Join(a.Scopes, " "), a.Status, now, now)
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
	expires_at, scopes, status, created_at, updated_at
FROM connector_accounts WHERE tenant_id = ? AND provider = ? AND account_label = ?`, tenantID, provider, label)
	var (
		a                ConnectorAccount
		access, refresh  []byte
		exp, created, up int64
		scopes           string
	)
	err := row.Scan(&a.Provider, &a.AccountLabel, &a.AuthKind, &access, &refresh, &a.TokenType, &exp, &scopes,
		&a.Status, &created, &up)
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
SELECT provider, account_label, auth_kind, token_type, expires_at, scopes, status, created_at, updated_at
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
		if err := rows.Scan(&a.Provider, &a.AccountLabel, &a.AuthKind, &a.TokenType, &exp, &scopes, &a.Status,
			&created, &up); err != nil {
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

// SetConnectorAccountStatus changes the status of one credential without
// touching its tokens. It returns ErrNotFound when no row matched.
func (s *Store) SetConnectorAccountStatus(ctx context.Context, tenantID, provider, label, status string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	switch status {
	case AccountOK, AccountExpired, AccountRevoked:
	default:
		return fmt.Errorf("store: invalid connector account status %q", status)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE connector_accounts SET status = ?, updated_at = ?
WHERE tenant_id = ? AND provider = ? AND account_label = ?`, status, s.timestamp(), tenantID, provider, label)
	if err != nil {
		return fmt.Errorf("store: set connector account status: %w", err)
	}
	return requireAffected(res)
}

// ConnectorClient is an OAuth client registration saved through the API.
// ClientSecret is plaintext in memory and AES-256-GCM encrypted at rest.
type ConnectorClient struct {
	Provider     string    `json:"provider"`
	ClientID     string    `json:"clientId"`
	ClientSecret string    `json:"-"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func clientAAD(tenantID, provider string) []byte {
	return []byte("lydiabox/connector_clients/v1\x00" + tenantID + "\x00" + provider)
}

// PutConnectorClient stores or replaces the OAuth client of a provider.
func (s *Store) PutConnectorClient(ctx context.Context, tenantID string, c ConnectorClient) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	if c.Provider == "" || c.ClientID == "" {
		return errors.New("store: connector client provider and client ID are required")
	}
	secret, err := s.cipher.EncryptString(c.ClientSecret, clientAAD(tenantID, c.Provider))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO connector_clients (tenant_id, provider, client_id, client_secret_enc, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, provider) DO UPDATE SET
	client_id = excluded.client_id, client_secret_enc = excluded.client_secret_enc, updated_at = excluded.updated_at`,
		tenantID, c.Provider, c.ClientID, secret, s.timestamp())
	if err != nil {
		return fmt.Errorf("store: put connector client: %w", err)
	}
	return nil
}

// GetConnectorClient returns the stored OAuth client of a provider or ErrNotFound.
func (s *Store) GetConnectorClient(ctx context.Context, tenantID, provider string) (*ConnectorClient, error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, err
	}
	var (
		c      ConnectorClient
		secret []byte
		up     int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT provider, client_id, client_secret_enc, updated_at FROM connector_clients
WHERE tenant_id = ? AND provider = ?`, tenantID, provider).Scan(&c.Provider, &c.ClientID, &secret, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get connector client: %w", err)
	}
	if c.ClientSecret, err = s.cipher.DecryptString(secret, clientAAD(tenantID, provider)); err != nil {
		return nil, err
	}
	c.UpdatedAt = fromMillis(up)
	return &c, nil
}

// DeleteConnectorClient removes the stored OAuth client or returns ErrNotFound.
func (s *Store) DeleteConnectorClient(ctx context.Context, tenantID, provider string) error {
	if err := checkTenant(tenantID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM connector_clients WHERE tenant_id = ? AND provider = ?`, tenantID, provider)
	if err != nil {
		return fmt.Errorf("store: delete connector client: %w", err)
	}
	return requireAffected(res)
}

// ConnectorAuditQuery filters ListConnectorAudit. Provider matches the
// "provider" field of the JSON detail; BeforeID pages backwards; Limit
// defaults to 100 and is capped at 1000.
type ConnectorAuditQuery struct {
	Provider string
	BeforeID int64
	Limit    int
}

// ListConnectorAudit returns "connector." audit entries newest first. It
// reads one row past Limit so that more reports whether another page exists.
func (s *Store) ListConnectorAudit(ctx context.Context, tenantID string, q ConnectorAuditQuery) (entries []AuditEntry, more bool, err error) {
	if err := checkTenant(tenantID); err != nil {
		return nil, false, err
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, detail, created_at FROM audit_log
WHERE tenant_id = ? AND substr(kind, 1, 10) = 'connector.' AND (? = 0 OR id < ?)
	AND (? = '' OR (json_valid(detail) AND json_extract(detail, '$.provider') = ?))
ORDER BY id DESC LIMIT ?`, tenantID, q.BeforeID, q.BeforeID, q.Provider, q.Provider, q.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("store: list connector audit: %w", err)
	}
	defer rows.Close()
	entries = []AuditEntry{}
	for rows.Next() {
		var (
			e  AuditEntry
			ts int64
		)
		if err := rows.Scan(&e.ID, &e.Kind, &e.Detail, &ts); err != nil {
			return nil, false, fmt.Errorf("store: list connector audit: %w", err)
		}
		e.CreatedAt = fromMillis(ts)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("store: list connector audit: %w", err)
	}
	if len(entries) > q.Limit {
		return entries[:q.Limit], true, nil
	}
	return entries, false, nil
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
