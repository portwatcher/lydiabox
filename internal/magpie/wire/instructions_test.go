package wire

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeInstructions(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", Instructions},
		{"blank", "\n\n", Instructions},
		{"no newline", "user", "user\n\n" + Instructions},
		{"newline", "user\n", "user\n\n" + Instructions},
		{"blank line", "user\n\n", "user\n\n" + Instructions},
		{"replace", "a\n<!-- lydiabox:begin old -->\nold\n<!-- lydiabox:end -->\nb\n", "a\n" + Instructions + "b\n"},
		{"replace indented", "a\n  <!-- lydiabox:begin -->\r\nold\r\n  <!-- lydiabox:end -->\r\nb", "a\n" + Instructions + "b"},
		{"current", "a\n\n" + Instructions + "\nb\n", "a\n\n" + Instructions + "\nb\n"},
		{"dangling begin", "a\n<!-- lydiabox:begin -->\nmine\n", "a\nmine\n\n" + Instructions},
		{"end only", "a\n<!-- lydiabox:end -->\n", "a\n<!-- lydiabox:end -->\n\n" + Instructions},
	}
	for _, c := range cases {
		got := mergeInstructions(c.in)
		if got != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
		if again := mergeInstructions(got); again != got {
			t.Errorf("%s: not idempotent:\n%q", c.name, again)
		}
		if n := strings.Count(got, instructionsBegin); n != 1 {
			t.Errorf("%s: %d blocks", c.name, n)
		}
	}
}

func TestInstructionsInEveryHarness(t *testing.T) {
	if len(InstructionFiles) != len(All()) {
		t.Fatalf("%d instruction files for %d harnesses", len(InstructionFiles), len(All()))
	}
	for _, w := range All() {
		home := t.TempDir()
		if err := w.Apply(DirHome(home), Binding{}); err != nil {
			t.Fatalf("%s: %v", w.Harness(), err)
		}
		ip, ok := w.(interface{ InstructionsPath() string })
		if !ok {
			t.Fatalf("%s has no instruction file", w.Harness())
		}
		rel := strings.TrimPrefix(ip.InstructionsPath(), "/home/agent/")
		b, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil || string(b) != Instructions {
			t.Errorf("%s: %s: %q %v", w.Harness(), rel, b, err)
		}
		for _, want := range []string{"lbx browser snapshot", "lbx computer screenshot", "browser_*", "takeover"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: block misses %q", w.Harness(), want)
			}
		}
	}
	// A binary file is not changed.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".dsh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".dsh/AGENTS.md"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstructions(DirHome(home), ".dsh/AGENTS.md"); err == nil {
		t.Error("binary instruction file was edited")
	}
}
