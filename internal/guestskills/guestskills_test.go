package guestskills

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// validName is the portable skill name of the Agent Skills format
// (lowercase letters, digits, and hyphens; Claude Code and OpenCode
// recommend at most 64 characters).
var validName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestEmbeddedSkills(t *testing.T) {
	skills := All()
	want := []string{"lydiabox-cli-hub", "lydiabox-install-software"}
	if len(skills) != len(want) {
		t.Fatalf("%d skills, want %d", len(skills), len(want))
	}
	for i, s := range skills {
		if s.Name != want[i] {
			t.Errorf("skill %d is %q, want %q", i, s.Name, want[i])
		}
		if !validName.MatchString(s.Name) || len(s.Name) > 64 {
			t.Errorf("%s: not a portable skill name", s.Name)
		}
		// Every value must be a plain YAML scalar: no ": " and no
		// leading YAML indicator.
		if len(s.Description) > 500 || strings.Contains(s.Description, ": ") || strings.ContainsAny(s.Description[:1], "&*!|>'\"%@`[]{}#") {
			t.Errorf("%s: description is not a short plain YAML scalar: %q", s.Name, s.Description)
		}
		if !Managed(s.Content) {
			t.Errorf("%s: no marker", s.Name)
		}
		if lines := strings.Count(string(s.Content), "\n"); lines > 120 {
			t.Errorf("%s: %d lines; keep a skill short", s.Name, lines)
		}
	}
	if !strings.Contains(string(skills[0].Content), "Apache License 2.0") {
		t.Error("lydiabox-cli-hub has no Apache-2.0 attribution")
	}
}

func TestParseRejects(t *testing.T) {
	ok := "---\nname: lydiabox-x\ndescription: Does x.\n---\n\n" + Marker + "\n"
	cases := map[string]struct{ dir, content string }{
		"no frontmatter": {"lydiabox-x", "# x\n"},
		"not closed":     {"lydiabox-x", "---\nname: lydiabox-x\n"},
		"other name":     {"lydiabox-x", strings.Replace(ok, "name: lydiabox-x", "name: lydiabox-y", 1)},
		"no prefix":      {"x", strings.Replace(ok, "name: lydiabox-x", "name: x", 1)},
		"no description": {"lydiabox-x", strings.Replace(ok, "description: Does x.", "description:", 1)},
		"no marker":      {"lydiabox-x", strings.Replace(ok, Marker, "", 1)},
		"bad line":       {"lydiabox-x", strings.Replace(ok, "description: Does x.", "description: Does x.\nfree text", 1)},
	}
	for name, c := range cases {
		if _, err := parse(c.dir, []byte(c.content)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := parse("lydiabox-x", []byte(ok)); err != nil {
		t.Errorf("valid skill: %v", err)
	}
	if _, err := load(fstest.MapFS{"skills/lydiabox-x/README.md": {Data: []byte("x")}}); err == nil {
		t.Error("a skill directory without SKILL.md loaded")
	}
}
