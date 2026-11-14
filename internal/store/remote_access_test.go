package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRemoteAccess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.GetRemoteAccess(ctx, "default", "cloudflare"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	ra := RemoteAccess{Provider: "cloudflare", State: []byte(`{"hostname":"a.example.com"}`), Secrets: []byte(`{"tunnelToken":"tok-secret"}`)}
	if err := s.PutRemoteAccess(ctx, "default", ra); err != nil {
		t.Fatal(err)
	}
	ra.State = []byte(`{"hostname":"b.example.com"}`)
	if err := s.PutRemoteAccess(ctx, "default", ra); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRemoteAccess(ctx, "default", "cloudflare")
	if err != nil || string(got.State) != `{"hostname":"b.example.com"}` || string(got.Secrets) != `{"tunnelToken":"tok-secret"}` || got.UpdatedAt.IsZero() {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if _, err := s.GetRemoteAccess(ctx, "other", "cloudflare"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remote access must be tenant scoped, got %v", err)
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT secrets_enc FROM remote_access`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("tok-secret")) {
		t.Fatal("secrets are stored in plain text")
	}
	// A ciphertext copied to another tenant fails authentication.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO remote_access SELECT 'other', provider, state_json, secrets_enc, created_at, updated_at FROM remote_access`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRemoteAccess(ctx, "other", "cloudflare"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("copied ciphertext: %v", err)
	}
	if err := s.PutRemoteAccess(ctx, "default", RemoteAccess{Provider: "cloudflare"}); err == nil {
		t.Fatal("want error for empty state")
	}
	if err := s.PutRemoteAccess(ctx, "", ra); err == nil {
		t.Fatal("want error for empty tenant")
	}
	if err := s.DeleteRemoteAccess(ctx, "default", "cloudflare"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRemoteAccess(ctx, "default", "cloudflare"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
