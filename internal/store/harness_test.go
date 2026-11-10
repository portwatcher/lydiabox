package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestMagpieSecrets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.GetMagpieSecret(ctx, "default", "gateway-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing secret: got %v, want ErrNotFound", err)
	}
	if err := s.PutMagpieSecret(ctx, "default", "gateway-key", "lbx-secret-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMagpieSecret(ctx, "default", "gateway-key", "lbx-secret-2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMagpieSecret(ctx, "default", "gateway-key")
	if err != nil || got != "lbx-secret-2" {
		t.Fatalf("get: %q, %v", got, err)
	}
	if _, err := s.GetMagpieSecret(ctx, "other", "gateway-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("secrets must be tenant scoped, got %v", err)
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT secret_enc FROM magpie_secrets`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("lbx-secret-2")) {
		t.Fatal("secret stored in plaintext")
	}
	if err := s.PutMagpieSecret(ctx, "", "x", "y"); err == nil {
		t.Fatal("empty tenant accepted")
	}
	if err := s.DeleteMagpieSecret(ctx, "default", "gateway-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMagpieSecret(ctx, "default", "gateway-key"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
