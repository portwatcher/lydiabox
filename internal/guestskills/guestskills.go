// Package guestskills holds the agent skills that LydiaBox gives to every
// harness in the guest VM (docs/design.md section 4.4.3). Each skill is a
// directory with a SKILL.md file (YAML frontmatter with name and
// description, then Markdown), the format that Claude Code, Codex,
// OpenCode, dsh, and Antigravity read. The wirers in internal/magpie/wire
// write them into the agent home before each harness start.
package guestskills

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// Prefix starts the name of every skill that LydiaBox owns. LydiaBox
// writes only directories with this prefix.
const Prefix = "lydiabox-"

// Marker is in every managed SKILL.md. A SKILL.md in a LydiaBox skill
// directory without it belongs to the user and is not changed.
const Marker = "<!-- Managed by LydiaBox; replaced at each harness start. -->"

// FileName is the skill file in a skill directory.
const FileName = "SKILL.md"

//go:embed skills
var embedded embed.FS

// Skill is one embedded skill.
type Skill struct {
	// Name is the directory name and the frontmatter name.
	Name string
	// Description is the frontmatter description (one line).
	Description string
	// Content is the SKILL.md file.
	Content []byte
}

// All returns the skills sorted by name. It panics when an embedded skill
// is not valid (a test checks them).
var All = sync.OnceValue(func() []Skill {
	skills, err := load(embedded)
	if err != nil {
		panic(err)
	}
	return skills
})

func load(fsys fs.FS) ([]Skill, error) {
	entries, err := fs.ReadDir(fsys, "skills")
	if err != nil {
		return nil, err
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		content, err := fs.ReadFile(fsys, path.Join("skills", e.Name(), FileName))
		if err != nil {
			return nil, err
		}
		s, err := parse(e.Name(), content)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parse reads the frontmatter of a SKILL.md and checks the rules of a
// managed skill: the name is the directory name and starts with Prefix,
// the description is one non-empty line, and the file has Marker.
func parse(dir string, content []byte) (Skill, error) {
	rest, ok := bytes.CutPrefix(content, []byte("---\n"))
	if !ok {
		return Skill{}, fmt.Errorf("guestskills: %s: no frontmatter", dir)
	}
	front, _, ok := bytes.Cut(rest, []byte("\n---\n"))
	if !ok {
		return Skill{}, fmt.Errorf("guestskills: %s: frontmatter is not closed", dir)
	}
	s := Skill{Content: content}
	for _, line := range strings.Split(string(front), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return Skill{}, fmt.Errorf("guestskills: %s: frontmatter line %q is not a key and a value", dir, line)
		}
		switch strings.TrimSpace(k) {
		case "name":
			s.Name = strings.TrimSpace(v)
		case "description":
			s.Description = strings.TrimSpace(v)
		}
	}
	switch {
	case s.Name != dir:
		return Skill{}, fmt.Errorf("guestskills: %s: name %q is not the directory name", dir, s.Name)
	case !strings.HasPrefix(s.Name, Prefix):
		return Skill{}, fmt.Errorf("guestskills: %s: name does not start with %q", dir, Prefix)
	case s.Description == "":
		return Skill{}, fmt.Errorf("guestskills: %s: no description", dir)
	case !bytes.Contains(content, []byte(Marker)):
		return Skill{}, fmt.Errorf("guestskills: %s: no managed marker", dir)
	}
	return s, nil
}

// Managed reports whether an existing SKILL.md is LydiaBox's (it has
// Marker) and so may be replaced.
func Managed(content []byte) bool { return bytes.Contains(content, []byte(Marker)) }
