package store

import (
	"context"
	"testing"
)

func TestDismissedSessions(t *testing.T) {
	ctx := context.Background()
	s, _, _ := openStore(t)
	if got, err := s.ListDismissedSessions(ctx, "default"); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v, %v", got, err)
	}
	for _, d := range [][3]string{{"default", "claude-code", "a"}, {"default", "codex", "b"}, {"other", "claude-code", "a"}} {
		if err := s.DismissSession(ctx, d[0], d[1], d[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DismissSession(ctx, "default", "claude-code", "a"); err != nil {
		t.Fatal("a second dismissal renews the first:", err)
	}
	got, err := s.ListDismissedSessions(ctx, "default")
	if err != nil || len(got) != 2 || got[0].Harness != "claude-code" || got[0].SessionID != "a" || got[0].DismissedAt.IsZero() || got[1].SessionID != "b" {
		t.Fatalf("default = %+v, %v", got, err)
	}
	if err := s.UndismissSession(ctx, "default", "claude-code", "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.UndismissSession(ctx, "default", "claude-code", "absent"); err != nil {
		t.Fatal("an absent dismissal is no error:", err)
	}
	if got, _ := s.ListDismissedSessions(ctx, "default"); len(got) != 1 || got[0].SessionID != "b" {
		t.Fatalf("after undismiss = %+v", got)
	}
	if got, _ := s.ListDismissedSessions(ctx, "other"); len(got) != 1 {
		t.Fatalf("other tenant = %+v", got)
	}
	if err := s.DismissSession(ctx, "", "codex", "x"); err == nil {
		t.Fatal("an empty tenant must be refused")
	}
}
