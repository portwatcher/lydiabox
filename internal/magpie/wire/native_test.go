package wire

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// guardedHome fails every access to a credential file.
type guardedHome struct {
	t    *testing.T
	home DirHome
}

func isCredential(rel string) bool {
	rel = path.Clean(rel)
	for _, f := range CredentialFiles {
		if rel == f {
			return true
		}
	}
	for _, d := range CredentialDirs {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

func (g guardedHome) ReadFile(rel string) ([]byte, error) {
	if isCredential(rel) {
		g.t.Errorf("native wirer read the credential file %s", rel)
	}
	return g.home.ReadFile(rel)
}

func (g guardedHome) WriteFile(rel string, data []byte, perm fs.FileMode) error {
	if isCredential(rel) {
		g.t.Errorf("native wirer wrote the credential file %s", rel)
	}
	return g.home.WriteFile(rel, data, perm)
}

func TestNativeWirersRemoveGatewaySettings(t *testing.T) {
	for _, w := range All() {
		t.Run(w.Harness(), func(t *testing.T) {
			home := copyTree(t, "testdata/home")
			if err := w.Apply(DirHome(home), Binding{Model: "kimi/kimi-k3"}); err != nil {
				t.Fatal(err)
			}
			native := Binding{Native: true}
			if err := w.Apply(guardedHome{t: t, home: DirHome(home)}, native); err != nil {
				t.Fatal(err)
			}
			if env := w.Env(native); len(env) != 0 {
				t.Errorf("Env = %v, want none in guest-native mode", env)
			}
			err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(home, p)
				if isCredential(filepath.ToSlash(rel)) {
					return nil
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				s := string(b)
				for _, bad := range []string{LocalToken, DefaultGatewayURL, "kimi/kimi-k3", "LydiaBox (magpie)", "model_providers." + ProviderID, "gemini-api://kimi"} {
					if strings.Contains(s, bad) && !strings.HasSuffix(rel, ".md") {
						t.Errorf("%s still has %q after the native wiring:\n%s", rel, bad, s)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeWirersKeepMCPAndUserContent(t *testing.T) {
	home := copyTree(t, "testdata/home")
	h := guardedHome{t: t, home: DirHome(home)}
	for _, w := range All() {
		if err := w.Apply(h, Binding{Native: true, Model: "opus"}); err != nil {
			t.Fatalf("%s: %v", w.Harness(), err)
		}
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for rel, want := range map[string][]string{
		".claude/settings.json":                 {`"model": "opus"`, `"skipDangerousModePermissionPrompt": true`, `"BASH_DEFAULT_TIMEOUT_MS": "120000"`},
		".codex/config.toml":                    {`model = "opus"`, "[mcp_servers.lydiabox]", `approval_policy = "on-request"`},
		".config/opencode/opencode.json":        {`"lydiabox-connectors"`, `"theme": "tokyonight"`},
		".gemini/antigravity/mcp_config.json":   {`"serverUrl": "http://127.0.0.1:8910/mcp"`},
		".claude/CLAUDE.md":                     {instructionsBegin},
		".gemini/antigravity-cli/settings.json": {`"colorScheme": "dark"`},
	} {
		got := read(rel)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", rel, w, got)
			}
		}
	}
	// opencode names models "<provider>/<model>": a bare name is not set.
	if strings.Contains(read(".config/opencode/opencode.json"), `"model": "opus"`) {
		t.Error("opencode got a model without a provider")
	}
}

func TestNativeOpencodeModel(t *testing.T) {
	home := DirHome(t.TempDir())
	w, _ := For("opencode")
	if err := w.Apply(home, Binding{Native: true, Model: "openai/gpt-6"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := w.CurrentModel(home); m != "openai/gpt-6" {
		t.Errorf("model = %q", m)
	}
}

func TestTOMLRemoveTable(t *testing.T) {
	doc := parseTOML([]byte("model = \"x\"\n\n[model_providers.lydiabox]\nname = \"a\"\n\n# keep\n[tui]\nx = 1\n"))
	doc.removeTable("model_providers.lydiabox")
	doc.remove("", "model")
	if got, want := string(doc.bytes()), "\n# keep\n[tui]\nx = 1\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
