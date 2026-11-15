package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestVMMachine(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.GetVMMachine(ctx, "default", "instabox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
	m := VMMachine{Driver: "instabox", State: []byte(`{"machineId":"m_a"}`), Secrets: []byte(`{"portToken":"ibp_secret"}`)}
	if err := s.PutVMMachine(ctx, "default", m); err != nil {
		t.Fatal(err)
	}
	m.State = []byte(`{"machineId":"m_b"}`)
	if err := s.PutVMMachine(ctx, "default", m); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVMMachine(ctx, "default", "instabox")
	if err != nil || string(got.State) != `{"machineId":"m_b"}` || string(got.Secrets) != `{"portToken":"ibp_secret"}` || got.UpdatedAt.IsZero() {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if _, err := s.GetVMMachine(ctx, "other", "instabox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the record must be tenant scoped, got %v", err)
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT secrets_enc FROM vm_machines`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("ibp_secret")) {
		t.Fatal("secrets are stored in plain text")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO vm_machines SELECT 'other', driver, state_json, secrets_enc, created_at, updated_at FROM vm_machines`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVMMachine(ctx, "other", "instabox"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("copied ciphertext: %v", err)
	}
	if err := s.PutVMMachine(ctx, "default", VMMachine{Driver: "instabox"}); err == nil {
		t.Fatal("want error for empty state")
	}
	if err := s.DeleteVMMachine(ctx, "default", "instabox"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVMMachine(ctx, "default", "instabox"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
