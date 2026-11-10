package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestTenantSubnet(t *testing.T) {
	ctx := context.Background()
	s, path, key := openStore(t)
	if n, err := s.TenantSubnet(ctx, "default"); err != nil || n != 0 {
		t.Fatalf("default = %d %v", n, err)
	}
	want := map[string]int{"alice": 1, "bob": 2, "carol": 3}
	for _, id := range []string{"alice", "bob", "carol", "alice", "bob"} {
		if n, err := s.TenantSubnet(ctx, id); err != nil || n != want[id] {
			t.Fatalf("%s = %d %v, want %d", id, n, err, want[id])
		}
	}
	// A freed index (row removed) is reused; others stay.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM vm_networks WHERE tenant_id = 'bob'`); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.TenantSubnet(ctx, "dave"); n != 2 {
		t.Fatalf("dave = %d, want the freed index 2", n)
	}
	// Stable across a reopen.
	s.Close()
	s2, err := Open(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n, _ := s2.TenantSubnet(ctx, "carol"); n != 3 {
		t.Fatalf("carol after reopen = %d", n)
	}
	for i := 0; i < MaxTenantSubnet; i++ {
		if _, err := s2.TenantSubnet(ctx, fmt.Sprintf("t%03d", i)); err != nil {
			if !errors.Is(err, ErrNoFreeSubnet) || i != MaxTenantSubnet-3 {
				t.Fatalf("tenant %d: %v", i, err)
			}
			return
		}
	}
	t.Fatal("subnets were not exhausted")
}
