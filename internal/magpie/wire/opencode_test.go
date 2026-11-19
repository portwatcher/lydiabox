package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCodeImageInput checks that every gateway model entry declares
// image input (so opencode passes screenshot images to the model) and that
// modalities the user set are kept.
func TestOpenCodeImageInput(t *testing.T) {
	home := t.TempDir()
	cfg := `{"provider":{"lydiabox":{"models":{"text/only":{"modalities":{"input":["text"],"output":["text"]}},"old/model":{"name":"old"}}}}}`
	if err := os.MkdirAll(filepath.Join(home, ".config/opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, opencodeConfig), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	w, _ := For("opencode")
	if err := w.Apply(DirHome(home), Binding{Model: "codex/gpt-6.1-sol"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, opencodeConfig))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Provider map[string]struct {
			Models map[string]struct {
				Modalities struct{ Input []string } `json:"modalities"`
				Attachment bool                     `json:"attachment"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	ms := got.Provider[ProviderID].Models
	for _, id := range []string{"codex/gpt-6.1-sol", "old/model"} {
		if m := ms[id]; len(m.Modalities.Input) != 2 || m.Modalities.Input[1] != "image" || !m.Attachment {
			t.Errorf("%s: %+v", id, m)
		}
	}
	if m := ms["text/only"]; len(m.Modalities.Input) != 1 {
		t.Errorf("user modalities changed: %+v", m)
	}
}
