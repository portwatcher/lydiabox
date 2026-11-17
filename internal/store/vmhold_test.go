package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestVMHold(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if held, _, err := s.VMHold(ctx, "default"); err != nil || held {
		t.Fatalf("new store: %v, %v", held, err)
	}
	if err := s.SetVMHold(ctx, "default", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVMHold(ctx, "default", true); err != nil {
		t.Fatalf("second set: %v", err)
	}
	if held, _, _ := s.VMHold(ctx, "other"); held {
		t.Fatal("the hold must be tenant scoped")
	}
	s.Close()

	s, err = Open(ctx, path, testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	held, at, err := s.VMHold(ctx, "default")
	if err != nil || !held || at.IsZero() {
		t.Fatalf("after reopen: %v %v %v", held, at, err)
	}
	if err := s.SetVMHold(ctx, "default", false); err != nil {
		t.Fatal(err)
	}
	if held, _, _ := s.VMHold(ctx, "default"); held {
		t.Fatal("clear did not clear")
	}
	if err := s.SetVMHold(ctx, "", true); err == nil {
		t.Fatal("want an error for an empty tenant")
	}
}
