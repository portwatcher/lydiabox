package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDiscoveryCache(t *testing.T) {
	ctx := context.Background()
	s, _, _ := openStore(t)
	if got, err := s.ListDiscoveryCache(ctx, "default"); err != nil || len(got) != 0 {
		t.Fatalf("empty cache = %v, %v", got, err)
	}
	if err := s.PutDiscoveryCache(ctx, "default", "codex", json.RawMessage(`[{"nativeId":"a"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDiscoveryCache(ctx, "default", "codex", json.RawMessage(`[{"nativeId":"b"}]`)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDiscoveryCache(ctx, "other", "dsh", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListDiscoveryCache(ctx, "default")
	if err != nil || len(got) != 1 || got[0].Harness != "codex" || string(got[0].Payload) != `[{"nativeId":"b"}]` || got[0].UpdatedAt.IsZero() {
		t.Fatalf("cache = %+v, %v", got, err)
	}
	if err := s.PutDiscoveryCache(ctx, "", "codex", nil); err == nil {
		t.Fatal("an empty tenant must be refused")
	}
}
