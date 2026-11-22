package magpie

import (
	"context"
	"errors"
	"testing"
)

func TestNativeServiceMakesNoGatewayCalls(t *testing.T) {
	s := &Service{Native: true, NativeUsed: func(context.Context) []NativeModel {
		return []NativeModel{{"codex", "gpt-6.1"}, {"codex", "gpt-6.1"}, {"opencode", "openai/gpt-6"}, {"dsh", "bad id"}}
	}}
	ctx := context.Background()
	if g := s.GatewayInfo(); g.Mode != ModeGuestNative || g.Status != StateOff {
		t.Errorf("gateway %+v", g)
	}
	ms, err := s.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.Provider+":"+m.ID)
	}
	want := []string{"claude-code:opus", "claude-code:sonnet", "claude-code:haiku", "codex:gpt-6.1", "opencode:openai/gpt-6"}
	if len(got) != len(want) {
		t.Fatalf("models %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("models %v, want %v", got, want)
		}
	}
	if subs, err := s.ListSubscriptions(ctx); err != nil || len(subs) != 0 {
		t.Errorf("subscriptions %v %v", subs, err)
	}
	if _, err := s.StartLogin(ctx, StartLoginRequest{Kind: "claude"}); !errors.Is(err, ErrGuestNative) || !errors.Is(err, ErrConflict) {
		t.Errorf("sign-in: %v", err)
	}
	if _, err := s.CreateProvider(ctx, CreateProviderRequest{Kind: "anthropic", Name: "a", APIKey: "k"}); !errors.Is(err, ErrGuestNative) {
		t.Errorf("API key: %v", err)
	}
}
