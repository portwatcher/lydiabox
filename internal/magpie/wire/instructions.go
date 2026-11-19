package wire

import (
	"fmt"
	"strings"
)

// Every harness reads a global instruction file into each session. The
// wirers keep a marked LydiaBox block in it that tells the agent about the
// browser and computer use of the VM (the lbx CLI and the lydiabox MCP
// tools), so the agent knows about them without a prompt from the user.
// The block is replaced in place on each apply; text outside the markers is
// the user's and stays as it is.

// InstructionFiles are the global instruction files, relative to the home
// directory. Sources: Claude Code user memory (~/.claude/CLAUDE.md), Codex
// global AGENTS.md in CODEX_HOME, OpenCode global rules
// (~/.config/opencode/AGENTS.md), the dsh user-global chain
// ($DSH_HOME/AGENTS.md, dsh-agent-instructions), and the Antigravity
// global rules (~/.gemini/GEMINI.md, from the agy CLI help).
var InstructionFiles = map[string]string{
	"claude-code": ".claude/CLAUDE.md",
	"codex":       ".codex/AGENTS.md",
	"opencode":    ".config/opencode/AGENTS.md",
	"dsh":         ".dsh/AGENTS.md",
	"antigravity": ".gemini/GEMINI.md",
}

const (
	instructionsBegin = "<!-- lydiabox:begin"
	instructionsEnd   = "<!-- lydiabox:end"
)

// Instructions is the managed block, markers included.
const Instructions = `<!-- lydiabox:begin (managed by LydiaBox; this block is replaced on each start) -->
## LydiaBox computer and browser use

You run on a LydiaBox VM with a 1920x1080 desktop. The user can watch this
desktop and take it over. For web tasks, use the Google Chrome on this
desktop, so that the user sees what you do. A headless browser is not
visible to the user.

Browser: use ` + "`lbx browser <op>`" + ` in the shell, or the ` + "`browser_*`" + ` tools of
the ` + "`lydiabox`" + ` MCP server. They are the same operations.
- ` + "`lbx browser open <url>`" + ` opens a page (` + "`--new-tab`" + ` for a new tab).
- ` + "`lbx browser snapshot`" + ` lists the headings and the interactive elements
  with refs such as ` + "`[e5]`" + `.
- ` + "`lbx browser click e5`" + `, ` + "`lbx browser type e7 \"text\" --submit`" + `,
  ` + "`lbx browser fill e7 \"text\"`" + `, ` + "`lbx browser select e9 \"Option\"`" + `,
  ` + "`lbx browser press Enter`" + `.
- ` + "`lbx browser text`" + ` reads the page text. ` + "`lbx browser screenshot`" + ` saves a PNG
  and prints its path. ` + "`lbx browser eval \"<js>\"`" + ` runs JavaScript.
- ` + "`lbx browser tabs`" + `, ` + "`tab N`" + `, ` + "`close N`" + `, ` + "`back`" + `, ` + "`forward`" + `,
  ` + "`scroll down`" + `, ` + "`wait text \"Done\"`" + `.
Take a new snapshot after the page changes. Refs from an old page do not work.

Desktop (other applications): ` + "`lbx computer screenshot`" + ` (prints a PNG path),
` + "`lbx computer click X Y`" + `, ` + "`type \"text\"`" + `, ` + "`key ctrl+l`" + `, ` + "`scroll X Y down 3`" + `,
` + "`launch chrome|blender|terminal`" + `, or the ` + "`computer_*`" + ` MCP tools.

If an action reports a user takeover, the user controls the desktop. Wait,
then take a new snapshot or screenshot. ` + "`lbx browser help`" + ` and
` + "`lbx computer help`" + ` list all operations.
<!-- lydiabox:end -->
`

// mergeInstructions returns doc with the managed block replaced, or
// appended after a blank line when doc has none. A begin marker without an
// end marker is removed (only that line), and a new block is appended.
func mergeInstructions(doc string) string {
	lines := strings.SplitAfter(doc, "\n")
	begin, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if begin < 0 && strings.HasPrefix(t, instructionsBegin) {
			begin = i
		} else if begin >= 0 && strings.HasPrefix(t, instructionsEnd) {
			end = i
			break
		}
	}
	if begin >= 0 && end >= 0 {
		return strings.Join(lines[:begin], "") + Instructions + strings.Join(lines[end+1:], "")
	}
	if begin >= 0 {
		lines = append(lines[:begin], lines[begin+1:]...)
	}
	rest := strings.Join(lines, "")
	switch {
	case strings.TrimSpace(rest) == "":
		return Instructions
	case strings.HasSuffix(rest, "\n\n"):
	case strings.HasSuffix(rest, "\n"):
		rest += "\n"
	default:
		rest += "\n\n"
	}
	return rest + Instructions
}

// WriteInstructions puts the managed block into the instruction file rel.
func WriteInstructions(h Home, rel string) error {
	old, err := readOptional(h, rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(rel), err)
	}
	if strings.ContainsRune(string(old), 0) {
		return fmt.Errorf("%s: %w", guestPath(rel), ErrUnparseable)
	}
	return writeIfChanged(h, rel, old, []byte(mergeInstructions(string(old))), 0o644)
}

// instructed adds the instruction block to the config edits of a wirer.
type instructed struct {
	Wirer
	rel string
}

func withInstructions(w Wirer) Wirer {
	rel, ok := InstructionFiles[w.Harness()]
	if !ok {
		return w
	}
	return instructed{Wirer: w, rel: rel}
}

func (w instructed) Apply(h Home, b Binding) error {
	if err := w.Wirer.Apply(h, b); err != nil {
		return err
	}
	return WriteInstructions(h, w.rel)
}

// InstructionsPath is the guest path of the global instruction file.
func (w instructed) InstructionsPath() string { return guestPath(w.rel) }
