package wire

import (
	"fmt"
	"path"

	"github.com/lydiabox/lydiabox/internal/guestskills"
)

// The wirers also write the LydiaBox agent skills (internal/guestskills)
// into the global skill directory that each harness reads, before each
// harness start, in both modes. Only directories named lydiabox-* are
// written, and only a SKILL.md that is missing or that has the managed
// marker is replaced; a skill directory of the user is never read,
// changed, or deleted. Skill paths are not credential files
// (CredentialFiles, CredentialDirs).
//
// SkillDirs are the global skill directories per harness, relative to the
// home directory. Evidence (docs/design.md section 4.4.3):
//   - claude-code: ~/.claude/skills (Claude Code docs, "Personal" skills).
//   - codex: ~/.agents/skills ($HOME/.agents/skills, user scope of the
//     Codex skills docs; also in the installed codex binary).
//   - opencode: ~/.config/opencode/skills is OpenCode's own global
//     directory, but OpenCode also reads ~/.claude/skills and
//     ~/.agents/skills. It uses ~/.agents/skills, so a skill is written
//     once for Codex, OpenCode, and dsh.
//   - dsh: ~/.agents/skills ($DSH_AGENTS_HOME/skills, default ~/.agents,
//     in the dsh-skill-filesystem package).
//   - antigravity: ~/.gemini/config/skills (the Antigravity skills docs:
//     the global directory of the agy CLI and the app).
var SkillDirs = map[string][]string{
	"claude-code": {".claude/skills"},
	"codex":       {".agents/skills"},
	"opencode":    {".agents/skills"},
	"dsh":         {".agents/skills"},
	"antigravity": {".gemini/config/skills"},
}

// WriteSkills writes every LydiaBox skill below the skill directory root.
func WriteSkills(h Home, root string) error {
	for _, s := range guestskills.All() {
		rel := path.Join(root, s.Name, guestskills.FileName)
		old, err := readOptional(h, rel)
		if err != nil {
			return fmt.Errorf("read %s: %w", guestPath(rel), err)
		}
		if old != nil && !guestskills.Managed(old) {
			// The user replaced the file: it is theirs now.
			continue
		}
		if err := writeIfChanged(h, rel, old, s.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// skilled adds the skills to the config edits of a wirer.
type skilled struct {
	Wirer
	roots []string
}

func withSkills(w Wirer) Wirer {
	roots, ok := SkillDirs[w.Harness()]
	if !ok {
		return w
	}
	return skilled{Wirer: w, roots: roots}
}

func (w skilled) Apply(h Home, b Binding) error {
	if err := w.Wirer.Apply(h, b); err != nil {
		return err
	}
	for _, root := range w.roots {
		if err := WriteSkills(h, root); err != nil {
			return err
		}
	}
	return nil
}
