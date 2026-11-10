package harness

import (
	"fmt"
	"strings"
	"time"
)

// Managed block markers in AGENTS.md, CLAUDE.md, and GEMINI.md.
const (
	BlockStart = "<!-- LYDIABOX:HANDOFF:START -->"
	BlockEnd   = "<!-- LYDIABOX:HANDOFF:END -->"
)

// InstructionFiles get the managed handoff block.
var InstructionFiles = []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md"}

// managedBlock is the text between (and including) the markers.
const managedBlock = BlockStart + `
## LydiaBox handoff

This task was moved here from another agent harness. Before you do
anything else, read ` + "`.agent/HANDOFF.md`" + `: it has the original goal, the
modified files, the recent tool actions, and the open issues. ` + "`.agent/state.json`" + `
has the same data as JSON. Continue the task from where it stopped.
` + BlockEnd

// HandoffState is the machine-readable handoff (docs/api.md section 8).
type HandoffState struct {
	OriginalGoal       string    `json:"originalGoal"`
	ModifiedFiles      []string  `json:"modifiedFiles"`
	ToolActionsSummary []string  `json:"toolActionsSummary"`
	RecentMessages     []Message `json:"recentMessages"`
	UnresolvedIssues   []string  `json:"unresolvedIssues"`
	GitStatus          string    `json:"gitStatus"`
	GitDiffStat        string    `json:"gitDiffStat"`
}

// stateFile is .agent/state.json: the state plus where it came from.
type stateFile struct {
	HandoffState
	SourceSessionID string    `json:"sourceSessionId"`
	SourceHarness   string    `json:"sourceHarness"`
	TargetHarness   string    `json:"targetHarness"`
	TargetModel     string    `json:"targetModel,omitempty"`
	WorkspaceDir    string    `json:"workspaceDir"`
	GeneratedAt     time.Time `json:"generatedAt"`
}

// gitStatusFiles returns the paths of `git status --short` output.
func gitStatusFiles(status string) []string {
	var out []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if _, to, ok := strings.Cut(p, " -> "); ok {
			p = to
		}
		out = append(out, strings.Trim(p, `"`))
	}
	return out
}

// buildState merges the trajectory with the workspace git state.
func buildState(t *CanonicalTrajectory, gitStatus, gitDiffStat string) HandoffState {
	st := HandoffState{
		OriginalGoal:       t.OriginalGoal,
		ModifiedFiles:      append([]string{}, t.ModifiedFiles...),
		ToolActionsSummary: append([]string{}, t.ToolActionsSummary...),
		RecentMessages:     append([]Message{}, t.RecentMessages...),
		UnresolvedIssues:   append([]string{}, t.UnresolvedIssues...),
		GitStatus:          gitStatus,
		GitDiffStat:        gitDiffStat,
	}
	seen := map[string]bool{}
	for _, f := range st.ModifiedFiles {
		seen[f] = true
	}
	for _, f := range gitStatusFiles(gitStatus) {
		if !seen[f] && !strings.HasPrefix(f, ".agent/") && !isInstructionFile(f) {
			seen[f] = true
			st.ModifiedFiles = append(st.ModifiedFiles, f)
		}
	}
	return st
}

func isInstructionFile(f string) bool {
	for _, n := range InstructionFiles {
		if f == n {
			return true
		}
	}
	return false
}

// RenderHandoff renders .agent/HANDOFF.md.
func RenderHandoff(st HandoffState, sourceID, target string, at time.Time) string {
	var b strings.Builder
	b.WriteString("# Handoff\n\n## Original goal\n\n")
	b.WriteString(orNone(st.OriginalGoal))
	b.WriteString("\n\n## Modified files\n\n")
	writeList(&b, st.ModifiedFiles, "`%s`")
	b.WriteString("\n## Recent tool actions\n\n")
	if len(st.ToolActionsSummary) == 0 {
		b.WriteString("None.\n")
	}
	for i, a := range st.ToolActionsSummary {
		fmt.Fprintf(&b, "%d. %s\n", i+1, a)
	}
	b.WriteString("\n## Recent conversation\n\n")
	if len(st.RecentMessages) == 0 {
		b.WriteString("None.\n")
	}
	for _, m := range st.RecentMessages {
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		fmt.Fprintf(&b, "**%s:**\n\n%s\n\n", role, quote(m.Text))
	}
	b.WriteString("## Unresolved issues\n\n")
	writeList(&b, st.UnresolvedIssues, "%s")
	b.WriteString("\n## Workspace state\n\n`git status --short`:\n\n")
	b.WriteString(fence(st.GitStatus))
	b.WriteString("\n`git diff --stat`:\n\n")
	b.WriteString(fence(st.GitDiffStat))
	fmt.Fprintf(&b, "\n## Next step\n\nContinue the original goal from the state above. Check the unresolved issues first.\n"+
		"Do not undo the modified files unless they are wrong.\n\n---\n\nSwitched from `%s` to `%s` by LydiaBox at %s.\n",
		sourceID, target, at.UTC().Format(time.RFC3339))
	return b.String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Unknown (the previous session has no recorded prompt)."
	}
	return s
}

func writeList(b *strings.Builder, items []string, format string) {
	if len(items) == 0 {
		b.WriteString("None.\n")
		return
	}
	for _, it := range items {
		fmt.Fprintf(b, "- "+format+"\n", it)
	}
}

// quote renders text as a Markdown block quote so headings inside messages
// do not break the document structure.
func quote(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

func fence(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		s = "(clean or not a git repository)"
	}
	f := "```"
	for strings.Contains(s, f) {
		f += "`"
	}
	return f + "text\n" + s + "\n" + f + "\n"
}

// UpsertManagedBlock returns content with exactly one managed block: an
// existing block is replaced in place, otherwise the block is appended.
func UpsertManagedBlock(content string) string {
	start := strings.Index(content, BlockStart)
	if start >= 0 {
		if end := strings.Index(content[start:], BlockEnd); end >= 0 {
			end += start + len(BlockEnd)
			rest := content[end:]
			// Remove any duplicate blocks after the first.
			for {
				s := strings.Index(rest, BlockStart)
				if s < 0 {
					break
				}
				e := strings.Index(rest[s:], BlockEnd)
				if e < 0 {
					break
				}
				rest = strings.TrimRight(rest[:s], "\n") + rest[s+e+len(BlockEnd):]
			}
			return content[:start] + managedBlock + rest
		}
	}
	if content == "" {
		return managedBlock + "\n"
	}
	return strings.TrimRight(content, "\n") + "\n\n" + managedBlock + "\n"
}
