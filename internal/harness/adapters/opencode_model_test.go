package adapters

import (
	"testing"

	"github.com/portwatcher/lydiabox/internal/magpie/wire"
)

// TestOCModel checks that an opencode session on the gateway provider
// reports the gateway model ID, as the other harnesses do.
func TestOCModel(t *testing.T) {
	if GatewayProvider != wire.ProviderID {
		t.Fatalf("GatewayProvider %q, wire.ProviderID %q", GatewayProvider, wire.ProviderID)
	}
	cases := []struct{ provider, model, want string }{
		{"lydiabox", "codex/gpt-6.1-sol", "codex/gpt-6.1-sol"},
		{"kimi", "kimi-k3", "kimi/kimi-k3"},
		{"", "gpt-5", "gpt-5"},
		{"lydiabox", "", "lydiabox"},
	}
	for _, tc := range cases {
		if got := ocModel(tc.provider, tc.model); got != tc.want {
			t.Errorf("ocModel(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
	msgs := []ocMessage{{Role: "assistant", ProviderID: "lydiabox", ModelID: "codex/gpt-6.1-sol"}}
	if m, _ := ocMeta("/home/agent", ocSession{ID: "ses_1"}, msgs); m.Model != "codex/gpt-6.1-sol" {
		t.Errorf("session model %q", m.Model)
	}
}
