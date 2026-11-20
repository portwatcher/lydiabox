package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func rawEvents(n, size int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		pad := strings.Repeat("x", max(0, size-len(fmt.Sprintf(`{"key":"e%d","text":""}`, i))))
		out[i] = json.RawMessage(fmt.Sprintf(`{"key":"e%d","text":"%s"}`, i, pad))
	}
	return out
}

func TestSessionEvents(t *testing.T) {
	ctx := context.Background()
	s, _, _ := openStore(t)
	if _, err := s.GetSessionEvents(ctx, "default", "codex", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get of an absent copy = %v", err)
	}
	if err := s.PutSessionEvents(ctx, "default", "codex", "a", rawEvents(3, 40)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSessionEvents(ctx, "default", "codex", "a", rawEvents(2, 40)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSessionEvents(ctx, "default", "codex", "a")
	if err != nil || len(got.Events) != 2 || got.Cut != 0 || got.UpdatedAt.IsZero() {
		t.Fatalf("copy = %+v, %v", got, err)
	}
	var e struct{ Key string }
	if err := json.Unmarshal(got.Events[1], &e); err != nil || e.Key != "e1" {
		t.Fatalf("event 1 = %s, %v", got.Events[1], err)
	}

	// Tenants are separate.
	if _, err := s.GetSessionEvents(ctx, "other", "codex", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other tenant = %v", err)
	}
	if err := s.PutSessionEvents(ctx, "other", "codex", "a", rawEvents(1, 40)); err != nil {
		t.Fatal(err)
	}
	stamps, err := s.ListSessionEventsStamps(ctx, "default")
	if err != nil || len(stamps) != 1 || stamps[0].Harness != "codex" || stamps[0].SessionID != "a" {
		t.Fatalf("stamps = %+v, %v", stamps, err)
	}
	if err := s.DeleteSessionEvents(ctx, "default", "codex", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionEvents(ctx, "default", "codex", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	if err := s.DeleteSessionEvents(ctx, "default", "codex", "a"); err != nil {
		t.Fatalf("delete of an absent copy = %v", err)
	}
	if got, err := s.GetSessionEvents(ctx, "other", "codex", "a"); err != nil || len(got.Events) != 1 {
		t.Fatalf("a delete removed the copy of another tenant: %+v, %v", got, err)
	}
	if err := s.PutSessionEvents(ctx, "", "codex", "a", nil); err == nil {
		t.Fatal("an empty tenant must be refused")
	}
}

// TestSessionEventsBound keeps the newest events inside the byte bound.
func TestSessionEventsBound(t *testing.T) {
	ctx := context.Background()
	s, _, _ := openStore(t)
	const size = 64 << 10
	n := MaxSessionEventsBytes/size + 10
	if err := s.PutSessionEvents(ctx, "default", "codex", "big", rawEvents(n, size)); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSessionEvents(ctx, "default", "codex", "big")
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, e := range got.Events {
		total += len(e) + 1
	}
	if total > MaxSessionEventsBytes || got.Cut == 0 || got.Cut+len(got.Events) != n {
		t.Fatalf("kept %d events (%d bytes), cut %d of %d", len(got.Events), total, got.Cut, n)
	}
	var last struct{ Key string }
	if err := json.Unmarshal(got.Events[len(got.Events)-1], &last); err != nil || last.Key != fmt.Sprintf("e%d", n-1) {
		t.Fatalf("the newest event is not kept: %q, %v", last.Key, err)
	}
}
