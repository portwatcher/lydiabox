package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCipherRoundTrip(t *testing.T) {
	c, err := NewCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range [][]byte{{}, []byte("x"), bytes.Repeat([]byte("refresh-token-"), 1000)} {
		ct, err := c.Encrypt(msg, []byte("aad"))
		if err != nil {
			t.Fatal(err)
		}
		if len(msg) > 0 && bytes.Contains(ct, msg) {
			t.Fatal("ciphertext contains plaintext")
		}
		pt, err := c.Decrypt(ct, []byte("aad"))
		if err != nil || !bytes.Equal(pt, msg) {
			t.Fatalf("round trip failed: %v", err)
		}
	}
	a, _ := c.Encrypt([]byte("same"), nil)
	b, _ := c.Encrypt([]byte("same"), nil)
	if bytes.Equal(a, b) {
		t.Fatal("nonces must be random")
	}
	if s, err := c.DecryptString(nil, nil); err != nil || s != "" {
		t.Fatal("empty ciphertext must decode to empty string")
	}
	if _, err := NewCipher(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key must be rejected")
	}
}

func TestCipherTamperDetection(t *testing.T) {
	c, _ := NewCipher(testKey(t))
	other, _ := NewCipher(testKey(t))
	ct, err := c.Encrypt([]byte("secret"), []byte("row-1"))
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"truncated":   ct[:len(ct)-1],
		"short":       ct[:10],
		"bad version": append([]byte{0x02}, ct[1:]...),
	}
	for i := 1; i < len(ct); i += 7 {
		m := bytes.Clone(ct)
		m[i] ^= 0x01
		mutations["bitflip"] = m
		if _, err := c.Decrypt(m, []byte("row-1")); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("bit flip at %d not detected", i)
		}
	}
	for name, m := range mutations {
		if _, err := c.Decrypt(m, []byte("row-1")); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%s: want ErrDecrypt, got %v", name, err)
		}
	}
	if _, err := c.Decrypt(ct, []byte("row-2")); !errors.Is(err, ErrDecrypt) {
		t.Error("wrong associated data must fail")
	}
	if _, err := other.Decrypt(ct, []byte("row-1")); !errors.Is(err, ErrDecrypt) {
		t.Error("wrong key must fail")
	}
}

func openStore(t *testing.T) (*Store, string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "lydiabox.db")
	key := testKey(t)
	s, err := Open(context.Background(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path, key
}

func TestConnectorAccounts(t *testing.T) {
	ctx := context.Background()
	s, path, _ := openStore(t)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	acct := ConnectorAccount{Provider: "github", AccountLabel: "octocat", AccessToken: "gho_access_SENTINEL",
		RefreshToken: "ghr_refresh_SENTINEL", TokenType: "bearer", ExpiresAt: exp, Scopes: []string{"repo", "workflow"}}
	if err := s.UpsertConnectorAccount(ctx, "default", acct); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnectorAccount(ctx, "default", "github", "octocat")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != acct.AccessToken || got.RefreshToken != acct.RefreshToken || !got.ExpiresAt.Equal(exp) ||
		len(got.Scopes) != 2 || got.AuthKind != "oauth2" || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}

	// Tenant isolation.
	if _, err := s.GetConnectorAccount(ctx, "other", "github", "octocat"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant: %v", err)
	}
	if list, _ := s.ListConnectorAccounts(ctx, "other", ""); len(list) != 0 {
		t.Fatal("other tenant must see no accounts")
	}
	if _, err := s.ListConnectorAccounts(ctx, "", ""); err == nil {
		t.Fatal("empty tenant must be rejected")
	}

	// Tokens are encrypted at rest, in the database file and the WAL.
	if err := s.VacuumInto(ctx, filepath.Join(t.TempDir(), "snap.db")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal"} {
		raw, _ := os.ReadFile(p)
		if bytes.Contains(raw, []byte("SENTINEL")) {
			t.Fatalf("%s contains a plaintext token", p)
		}
	}

	// A ciphertext moved to another row fails authentication.
	if err := s.UpsertConnectorAccount(ctx, "default", ConnectorAccount{Provider: "github", AccountLabel: "mallory", AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE connector_accounts SET access_token_enc =
		(SELECT access_token_enc FROM connector_accounts WHERE account_label = 'octocat') WHERE account_label = 'mallory'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConnectorAccount(ctx, "default", "github", "mallory"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("swapped ciphertext: want ErrDecrypt, got %v", err)
	}

	list, err := s.ListConnectorAccounts(ctx, "default", "github")
	if err != nil || len(list) != 2 || list[0].AccessToken != "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.DeleteConnectorAccount(ctx, "default", "github", "octocat"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConnectorAccount(ctx, "default", "github", "octocat"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestReopenWithWrongKey(t *testing.T) {
	ctx := context.Background()
	s, path, key := openStore(t)
	if err := s.UpsertConnectorAccount(ctx, "default", ConnectorAccount{Provider: "slack", AccountLabel: "w", AccessToken: "xoxb"}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	same, err := Open(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := same.GetConnectorAccount(ctx, "default", "slack", "w"); err != nil || a.AccessToken != "xoxb" {
		t.Fatalf("reopen: %v", err)
	}
	same.Close()

	wrong, err := Open(ctx, path, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	if _, err := wrong.GetConnectorAccount(ctx, "default", "slack", "w"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong key: %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode = %o", fi.Mode().Perm())
	}
}

func TestBindingsAuditSessions(t *testing.T) {
	ctx := context.Background()
	s, _, _ := openStore(t)

	if err := s.SetHarnessBinding(ctx, "default", "codex", "deepseek/v4"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHarnessBinding(ctx, "default", "codex", "kimi/k3"); err != nil {
		t.Fatal(err)
	}
	if b, err := s.GetHarnessBinding(ctx, "default", "codex"); err != nil || b.Model != "kimi/k3" {
		t.Fatalf("binding = %+v, %v", b, err)
	}
	if _, err := s.GetHarnessBinding(ctx, "t2", "codex"); !errors.Is(err, ErrNotFound) {
		t.Fatal("binding leaked across tenants")
	}
	if list, _ := s.ListHarnessBindings(ctx, "default"); len(list) != 1 {
		t.Fatalf("bindings = %+v", list)
	}
	if err := s.DeleteHarnessBinding(ctx, "default", "codex"); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"connector.token.issued", "connector.connected", "vm.boot"} {
		if _, err := s.AppendAudit(ctx, "default", k, `{"provider":"github"}`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendAudit(ctx, "t2", "connector.connected", ""); err != nil {
		t.Fatal(err)
	}
	entries, err := s.ListAudit(ctx, "default", AuditQuery{KindPrefix: "connector."})
	if err != nil || len(entries) != 2 || entries[0].Kind != "connector.connected" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
	page, _ := s.ListAudit(ctx, "default", AuditQuery{BeforeID: entries[0].ID, Limit: 1})
	if len(page) != 1 || page[0].ID != entries[1].ID {
		t.Fatalf("page = %+v", page)
	}

	m := SessionMeta{Harness: "claude-code", SessionID: "s1", WorkspaceDir: "/workspace/p", Title: "t", Extra: json.RawMessage(`{"a":1}`)}
	if err := s.UpsertSessionMeta(ctx, "default", m); err != nil {
		t.Fatal(err)
	}
	m.Title = "renamed"
	if err := s.UpsertSessionMeta(ctx, "default", m); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSessionMeta(ctx, "default", SessionMeta{Harness: "codex", SessionID: "s2", Extra: json.RawMessage(`not json`)}); err == nil {
		t.Fatal("invalid extra JSON must be rejected")
	}
	sessions, err := s.ListSessionMeta(ctx, "default", "claude-code")
	if err != nil || len(sessions) != 1 || sessions[0].Title != "renamed" || string(sessions[0].Extra) != `{"a":1}` {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	if err := s.DeleteSessionMeta(ctx, "default", "claude-code", "s1"); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationsAndVacuum(t *testing.T) {
	ctx := context.Background()
	s, path, key := openStore(t)
	if _, err := s.AppendAudit(ctx, "default", "x", "y"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(ctx, path, key) // re-applying migrations must be a no-op
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var n int
	if err := s2.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(sortedMigrations()) {
		t.Fatalf("schema_migrations rows = %d, %v", n, err)
	}

	dest := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s2.VacuumInto(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := s2.VacuumInto(ctx, dest); err == nil {
		t.Fatal("VacuumInto must refuse an existing target")
	}
	snap, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if err := snap.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshot audit rows = %d, %v", n, err)
	}
}

func TestSplitStatements(t *testing.T) {
	got := splitStatements("CREATE TABLE a (x INT);\n\nCREATE INDEX i ON a (x);\n")
	if len(got) != 2 || got[1] != "CREATE INDEX i ON a (x)" {
		t.Fatalf("got %q", got)
	}
}
