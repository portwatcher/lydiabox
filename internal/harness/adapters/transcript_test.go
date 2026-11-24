package adapters_test

import (
	"errors"
	"testing"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
	"github.com/portwatcher/lydiabox/internal/harness/adapters/adapterstest"
)

// step is "role tool text-prefix" of one transcript entry.
type step struct{ role, tool, text string }

func TestTranscripts(t *testing.T) {
	home := adapterstest.Home(t)
	u, a := harness.RoleUser, harness.RoleAssistant
	tool := func(name, text string) step { return step{harness.RoleTool, name, text} }
	cases := []struct {
		name string
		tr   harness.Transcriber
		id   string
		want []step
	}{
		{"claude-code", adapters.Claude{}, adapterstest.ClaudeID, []step{
			{u, "", "Make uploads in the File Manager resumable."}, {a, "", "I'll look at the upload handler first."},
			tool("Read", "/workspace/lydiabox/internal/storage/files/http.go"), tool("Edit", "/workspace/lydiabox/internal/storage/files/http.go"),
			tool("Bash", "go test ./internal/storage/..."), {a, "", "The resume test fails"}, {u, "", "Also keep the upload size limit at 10 GiB."},
		}},
		{"codex", adapters.Codex{}, adapterstest.CodexID, []step{
			{u, "", "Fix the flaky upload test"}, tool("shell", "bash -lc go test"), tool("apply_patch", "*** Begin Patch"),
			tool("shell", "bash -lc go test"), {a, "", "The test now passes"},
		}},
		{"codex legacy", adapters.Codex{}, adapterstest.CodexLegacyID, []step{
			{u, "", "Add a README section"}, tool("shell", "bash -lc grep -n WebDAV README.md"), {a, "", "Added a WebDAV section"},
		}},
		{"dsh", adapters.DSH{}, adapterstest.DSHID, []step{
			{u, "", "Add retry with exponential backoff"}, {a, "", "I will read metasync.go first."},
			tool("read", "/workspace/lydiabox/internal/storage/metasync.go"), tool("edit", "/workspace/lydiabox/internal/storage/metasync.go"),
			tool("bash", "go vet ./internal/storage/"), {a, "", "go vet reports unreachable code"},
		}},
		{"antigravity", adapters.Antigravity{}, adapterstest.AntigravityID, []step{
			{u, "", "Add a dark mode toggle"}, {a, "", "I will add a ThemeToggle component"},
			tool("view", "/workspace/lydiabox/web/src/components/Header.tsx"), tool("edit", "/workspace/lydiabox/web/src/components/ThemeToggle.tsx"),
			{a, "", "The build fails because theme.ts does not exist yet."},
		}},
		{"opencode", adapters.OpenCode{}, adapterstest.OpenCodeID, []step{
			{u, "", "Write docs/pty.md"}, {a, "", "I'll draft the document"}, tool("write", "/workspace/lydiabox/docs/pty.md"),
			tool("bash", "markdownlint docs/pty.md"), {a, "", "docs/pty.md is written."},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.tr.Transcript(home, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Entries) != len(tc.want) {
				t.Fatalf("%d entries, want %d: %+v", len(got.Entries), len(tc.want), got.Entries)
			}
			for i, w := range tc.want {
				g := got.Entries[i]
				if g.Role != w.role || g.Tool != w.tool || len(g.Text) < len(w.text) || g.Text[:len(w.text)] != w.text {
					t.Errorf("entry %d = %+v, want %+v", i, g, w)
				}
			}
			if _, err := tc.tr.Transcript(home, "../../etc/passwd"); !errors.Is(err, harness.ErrBadRequest) {
				t.Errorf("path traversal ID: %v", err)
			}
			if _, err := tc.tr.Transcript(home, "missing-session"); !errors.Is(err, harness.ErrNotFound) {
				t.Errorf("unknown ID: %v", err)
			}
		})
	}
}
