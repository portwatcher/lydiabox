package wire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDSHFallbackModel checks that dsh gets a model that the gateway serves
// when no model was ever bound, and that a bound model or one in the env
// file wins over the fallback.
func TestDSHFallbackModel(t *testing.T) {
	calls := 0
	fallback := func() string { calls++; return "codex/gpt-6.1-sol" }
	home := t.TempDir()
	w, _ := For("dsh")
	if err := w.Apply(DirHome(home), Binding{Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.CurrentModel(DirHome(home)); m != "codex/gpt-6.1-sol" || calls != 1 {
		t.Fatalf("model %q after %d fallback calls", m, calls)
	}
	// The model in lydiabox.env stays; the fallback is not asked again.
	if err := w.Apply(DirHome(home), Binding{Fallback: func() string { calls++; return "other/x" }}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.CurrentModel(DirHome(home)); m != "codex/gpt-6.1-sol" || calls != 1 {
		t.Fatalf("model %q after %d fallback calls", m, calls)
	}
	// A bound model wins.
	if err := w.Apply(DirHome(home), Binding{Model: "kimi/kimi-k3", Fallback: fallback}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.CurrentModel(DirHome(home)); m != "kimi/kimi-k3" || calls != 1 {
		t.Fatalf("model %q after %d fallback calls", m, calls)
	}
	// Without a gateway model no DSH_MODEL is written, so the launcher
	// adds no overlay; the overlay file holds no secret.
	empty := t.TempDir()
	if err := w.Apply(DirHome(empty), Binding{Fallback: func() string { return "" }}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(empty, dshEnv))
	patch, err := os.ReadFile(filepath.Join(empty, dshPatch))
	if err != nil || strings.Contains(string(env), "DSH_MODEL") {
		t.Fatalf("env %q, patch err %v", env, err)
	}
	if strings.Contains(string(patch), LocalToken) || !strings.Contains(string(patch), "apiKeyEnv: OPENAI_API_KEY") {
		t.Errorf("patch:\n%s", patch)
	}
}
