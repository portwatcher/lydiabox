package wire

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/golden")

// copyTree copies testdata/home into a fresh temporary home directory.
func copyTree(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestWirersPreserveUserContent(t *testing.T) {
	binding := Binding{Model: "kimi/kimi-k3"}
	cases := []struct {
		harness string
		files   []string
		model   string
		// keep are user fragments that must survive verbatim.
		keep []string
	}{
		{"claude-code", []string{".claude/settings.json", ".claude.json", ".claude/CLAUDE.md"}, "kimi/kimi-k3",
			[]string{"# My global rules\n\n- Answer in British English.\n- Run the tests before a commit.\n\n<!-- lydiabox:begin", `"Bash(git status:*)"`, `"BASH_DEFAULT_TIMEOUT_MS": "120000"`, `"numStartups": 42`,
				`"github-mcp-server"`, `"X-Trace": "1"`, `"/workspace/lydiabox"`, `"statusLine"`}},
		{"codex", []string{".codex/config.toml", ".codex/AGENTS.md"}, "kimi/kimi-k3",
			[]string{"# Codex notes\n\nPrefer small commits.\n\n<!-- lydiabox:begin (managed", "<!-- lydiabox:end -->\n\n## After the block\nKeep this section.\n", "# Personal Codex configuration. Keep this comment.", `approval_policy = "on-request"  # ask before running commands`,
				`model = "kimi/kimi-k3" # my default model`, `model = "gpt-5.2-mini"`, "# The fast profile keeps its own model.",
				`  "@acme/docs-mcp", # pinned`, `url = "http://127.0.0.1:8910/mcp" # stale port`, "startup_timeout_sec = 20",
				"[tui]\nnotifications = true"}},
		{"dsh", []string{".dsh/lydiabox.env", ".dsh/.env", ".dsh/AGENTS.md"}, "kimi/kimi-k3",
			[]string{"# dsh environment, edited by hand", "DSH_THEME=dark", "# trailing comment block"}},
		{"antigravity", []string{".antigravity/settings.json", ".gemini/antigravity-cli/settings.json", ".gemini/antigravity/mcp_config.json", ".gemini/config/mcp_config.json", ".gemini/GEMINI.md"}, "kimi/kimi-k3",
			[]string{`"theme": "solarized"`, `"telemetry": false`, `"HTTP_PROXY": "http://proxy.local:3128"`, `"@modelcontextprotocol/server-filesystem"`,
				`"colorScheme": "dark"`, `"trustedWorkspaces": [`, `"modelName": "gemini-api://gemini-3.1-pro-preview"`}},
		{"opencode", []string{".config/opencode/opencode.json", ".config/opencode/AGENTS.md"}, "kimi/kimi-k3",
			[]string{`"theme": "tokyonight"`, `"timeout": 600000`, `"db-mcp"`, `"leader": "ctrl+x"`}},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			home := copyTree(t, "testdata/home")
			w, ok := For(tc.harness)
			if !ok {
				t.Fatalf("no wirer for %s", tc.harness)
			}
			if err := w.Apply(DirHome(home), binding); err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, f := range tc.files {
				b, err := os.ReadFile(filepath.Join(home, f))
				if err != nil {
					t.Fatal(err)
				}
				got[f] = string(b)
				golden := filepath.Join("testdata/golden", tc.harness, f)
				if *update {
					if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(golden, b, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				if string(want) != string(b) {
					t.Errorf("%s differs from golden:\n%s", f, b)
				}
			}
			all := strings.Join(mapValues(got), "\n")
			for _, k := range tc.keep {
				if !strings.Contains(all, k) {
					t.Errorf("user content lost: %q", k)
				}
			}
			for _, s := range DefaultMCPServers {
				if tc.harness != "dsh" && !strings.Contains(all, s.URL) {
					t.Errorf("MCP server %s not registered", s.URL)
				}
			}
			m, err := w.CurrentModel(DirHome(home))
			if err != nil || m != tc.model {
				t.Fatalf("CurrentModel = %q, %v; want %q", m, err, tc.model)
			}
			// Idempotent: a second apply changes nothing.
			if err := w.Apply(DirHome(home), binding); err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.files {
				b, _ := os.ReadFile(filepath.Join(home, f))
				if string(b) != got[f] {
					t.Errorf("%s changed on the second apply:\n%s", f, b)
				}
			}
			// An empty model keeps the bound model.
			if err := w.Apply(DirHome(home), Binding{}); err != nil {
				t.Fatal(err)
			}
			if m, _ := w.CurrentModel(DirHome(home)); m != tc.model {
				t.Fatalf("empty binding changed the model to %q", m)
			}
		})
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestWirersCreateMissingFiles(t *testing.T) {
	for _, w := range All() {
		t.Run(w.Harness(), func(t *testing.T) {
			home := t.TempDir()
			if err := w.Apply(DirHome(home), Binding{Model: "deepseek/deepseek-v4-pro"}); err != nil {
				t.Fatal(err)
			}
			m, err := w.CurrentModel(DirHome(home))
			if err != nil || m != "deepseek/deepseek-v4-pro" {
				t.Fatalf("CurrentModel = %q, %v", m, err)
			}
			rel := strings.TrimPrefix(w.ConfigPath(), "/home/agent/")
			if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
				t.Fatalf("config %s not created: %v", rel, err)
			}
		})
	}
}

func TestWirersRefuseUnparseableAndSymlinks(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	bad := []byte("{ // comment\n \"model\": \"x\" }\n")
	if err := os.WriteFile(filepath.Join(home, ".claude/settings.json"), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	w, _ := For("claude-code")
	if err := w.Apply(DirHome(home), Binding{Model: "m"}); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("Apply on JSONC = %v, want ErrUnparseable", err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude/settings.json")); string(b) != string(bad) {
		t.Fatal("unparseable file was modified")
	}

	outside := t.TempDir()
	target := filepath.Join(outside, "victim.toml")
	if err := os.WriteFile(target, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".codex/config.toml")); err != nil {
		t.Fatal(err)
	}
	cw, _ := For("codex")
	if err := cw.Apply(DirHome(home), Binding{Model: "m"}); err == nil {
		t.Fatal("wirer followed a symlink out of the home directory")
	}
	if b, _ := os.ReadFile(target); string(b) != "x = 1\n" {
		t.Fatal("file outside the home directory was modified")
	}
}

func TestTOMLEditEdgeCases(t *testing.T) {
	cases := []struct{ name, in, table, key, value, want string }{
		{"empty", "", "", "model", `"m"`, "model = \"m\"\n"},
		{"root before first table", "[a]\nx = 1\n", "", "model", `"m"`, "model = \"m\"\n\n[a]\nx = 1\n"},
		{"after leading comment", "# head\n\n[a]\nx = 1\n", "", "model", `"m"`, "# head\n\nmodel = \"m\"\n\n[a]\nx = 1\n"},
		{"quoted table", "[mcp_servers.\"lydiabox\"]\nurl = \"old\"\n", "mcp_servers.lydiabox", "url", `"new"`, "[mcp_servers.\"lydiabox\"]\nurl = \"new\"\n"},
		{"multi-line value replaced", "[t]\nargs = [\n  \"a\",\n]\nz = 1\n", "t", "args", `["b"]`, "[t]\nargs = [\"b\"]\nz = 1\n"},
		{"new table appended", "a = 1\n", "x.y", "k", `"v"`, "a = 1\n\n[x.y]\nk = \"v\"\n"},
		{"no trailing newline", "a = 1", "", "a", "2", "a = 2\n"},
		{"string with hash", "a = \"x#y\" # c\n", "", "a", `"z"`, "a = \"z\" # c\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := parseTOML([]byte(tc.in))
			d.set(tc.table, tc.key, tc.value)
			if got := string(d.bytes()); got != tc.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
	if got := tomlString("a\"b\\c\x01"); got != `"a\"b\\c\u0001"` {
		t.Fatalf("tomlString = %s", got)
	}
}

func TestEnvEdit(t *testing.T) {
	d := parseEnv([]byte("A=1\nexport B='two words' # note\n# C=3\nB=again\n"))
	d.set("B", "x y")
	d.set("D", "plain")
	want := "A=1\nexport B=\"x y\" # note\n# C=3\nB=\"x y\"\nD=plain\n"
	if got := string(d.bytes()); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if v, ok := d.get("B"); !ok || v != "x y" {
		t.Fatalf("get B = %q %v", v, ok)
	}
	if _, ok := d.get("C"); ok {
		t.Fatal("commented variable must not be read")
	}
}

// TestClaudeFirstStart: a new home starts at the prompt (onboarding done,
// the persistent trees trusted); a value that the user set stays.
func TestClaudeFirstStart(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"),
		[]byte(`{"hasCompletedOnboarding": false, "projects": {"/workspace": {"hasTrustDialogAccepted": false, "allowedTools": ["Read"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, _ := For("claude-code")
	if err := w.Apply(DirHome(home), Binding{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Onboarding *bool `json:"hasCompletedOnboarding"`
		Projects   map[string]struct {
			Trust        *bool    `json:"hasTrustDialogAccepted"`
			AllowedTools []string `json:"allowedTools"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.Onboarding == nil || *st.Onboarding {
		t.Errorf("the user's onboarding value changed: %s", b)
	}
	if p := st.Projects["/workspace"]; p.Trust == nil || *p.Trust || len(p.AllowedTools) != 1 {
		t.Errorf("the user's /workspace entry changed: %s", b)
	}
	for _, dir := range []string{"/home/agent", "/data/workspace", "/data/lydiabox/home"} {
		if p := st.Projects[dir]; p.Trust == nil || !*p.Trust {
			t.Errorf("%s not trusted: %s", dir, b)
		}
	}

	fresh := t.TempDir()
	if err := w.Apply(DirHome(fresh), Binding{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(fresh, ".claude.json"))
	st.Onboarding, st.Projects = nil, nil
	if err := json.Unmarshal(b, &st); err != nil || st.Onboarding == nil || !*st.Onboarding || st.Projects["/workspace"].Trust == nil {
		t.Errorf("new home = %s, %v", b, err)
	}
}
