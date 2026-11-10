package adapters

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// maxMessageRunes bounds one recent message in a trajectory.
const maxMessageRunes = 4000

// builder accumulates a CanonicalTrajectory from transcript events in order.
type builder struct {
	workspace string
	goal      string
	messages  []harness.Message
	actions   []string
	modified  []string
	pending   map[string]string // tool call ID -> action summary
	issues    map[string]string // action summary -> issue text
	issueKeys []string
	// toolSince is set by a tool call so the next assistant text starts a
	// new message instead of joining the text before the call.
	toolSince bool
}

func newBuilder(workspace string) *builder {
	return &builder{workspace: workspace, pending: map[string]string{}, issues: map[string]string{}}
}

// user records a human prompt. The first one is the original goal.
func (b *builder) user(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if b.goal == "" {
		b.goal = text
	}
	b.add("user", text)
}

// assistant records assistant text. Consecutive assistant text parts of one
// turn are joined.
func (b *builder) assistant(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.add("assistant", text)
}

func (b *builder) add(role, text string) {
	brk := b.toolSince
	b.toolSince = false
	if n := len(b.messages); n > 0 && !brk && b.messages[n-1].Role == role && role == "assistant" {
		b.messages[n-1].Text = truncate(b.messages[n-1].Text+"\n\n"+text, maxMessageRunes)
		return
	}
	b.messages = append(b.messages, harness.Message{Role: role, Text: truncate(text, maxMessageRunes)})
}

// tool records a tool call and returns its summary.
func (b *builder) tool(callID, name string, args map[string]any) string {
	summary, files := summarizeTool(name, args, b.workspace)
	for _, f := range files {
		if !slices.Contains(b.modified, f) {
			b.modified = append(b.modified, f)
		}
	}
	b.actions = append(b.actions, summary)
	b.toolSince = true
	if callID != "" {
		b.pending[callID] = summary
	}
	return summary
}

// addModified records a changed file reported outside a tool call.
func (b *builder) addModified(f string) {
	if f != "" && !slices.Contains(b.modified, f) {
		b.modified = append(b.modified, f)
	}
}

// result records the outcome of a tool call. A failure stays an unresolved
// issue until the same action later succeeds.
func (b *builder) result(callID string, failed bool, output string) {
	summary, ok := b.pending[callID]
	if !ok {
		return
	}
	delete(b.pending, callID)
	b.outcome(summary, failed, output)
}

func (b *builder) outcome(summary string, failed bool, output string) {
	if !failed {
		if _, ok := b.issues[summary]; ok {
			delete(b.issues, summary)
			b.issueKeys = slices.DeleteFunc(b.issueKeys, func(k string) bool { return k == summary })
		}
		return
	}
	issue := summary + " failed"
	if line := firstLine(output); line != "" {
		issue += ": " + truncate(line, 200)
	}
	if _, ok := b.issues[summary]; !ok {
		b.issueKeys = append(b.issueKeys, summary)
	}
	b.issues[summary] = issue
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// finish returns the trajectory with the design limits applied.
func (b *builder) finish(harnessName, sessionID, model string) *harness.CanonicalTrajectory {
	t := &harness.CanonicalTrajectory{
		Harness: harnessName, SessionID: sessionID, WorkspaceDir: b.workspace, Model: model,
		OriginalGoal:       b.goal,
		ModifiedFiles:      append([]string{}, b.modified...),
		ToolActionsSummary: lastN(b.actions, harness.MaxToolActions),
		RecentMessages:     lastN(b.messages, harness.MaxRecentMessage),
		UnresolvedIssues:   []string{},
	}
	for _, k := range b.issueKeys {
		t.UnresolvedIssues = append(t.UnresolvedIssues, b.issues[k])
	}
	if n := len(b.messages); n > 0 && b.messages[n-1].Role == "user" {
		t.UnresolvedIssues = append(t.UnresolvedIssues, "Unanswered request: "+truncate(firstLine(b.messages[n-1].Text), 200))
	}
	return t
}

func lastN[T any](s []T, n int) []T {
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return append([]T{}, s...)
}

var patchFileRe = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			parts := make([]string, 0, len(v))
			for _, p := range v {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			if len(parts) > 0 {
				return shellCommand(parts)
			}
		}
	}
	return ""
}

// shellCommand renders an argv, unwrapping `bash -lc "<script>"`.
func shellCommand(argv []string) string {
	if len(argv) == 3 && (argv[0] == "bash" || argv[0] == "sh" || argv[0] == "zsh") && strings.HasPrefix(argv[1], "-") {
		return argv[2]
	}
	return strings.Join(argv, " ")
}

// relPath makes p relative to the workspace when it is inside it.
func relPath(p, workspace string) string {
	if workspace != "" && path.IsAbs(p) {
		if rel, ok := strings.CutPrefix(path.Clean(p), strings.TrimRight(workspace, "/")+"/"); ok {
			return rel
		}
	}
	return p
}

// summarizeTool turns a tool call into a short action ("edit a.go", "run go
// test ./...") and the files it modifies.
func summarizeTool(name string, args map[string]any, workspace string) (string, []string) {
	n := strings.ToLower(name)
	if args == nil {
		args = map[string]any{}
	}
	file := str(args, "file_path", "filePath", "path", "notebook_path", "target_file", "file", "absolute_path")
	switch {
	case n == "apply_patch" || n == "patch" || strings.Contains(str(args, "input", "patch", "command"), "*** Begin Patch"):
		var files []string
		for _, m := range patchFileRe.FindAllStringSubmatch(str(args, "input", "patch", "command"), -1) {
			files = append(files, relPath(strings.TrimSpace(m[1]), workspace))
		}
		if len(files) == 0 && file != "" {
			files = []string{relPath(file, workspace)}
		}
		return "edit " + strings.Join(files, ", "), files
	case isShellTool(n):
		cmd := str(args, "command", "cmd", "script", "input")
		return "run " + truncate(strings.Join(strings.Fields(cmd), " "), 160), nil
	case isEditTool(n):
		if file == "" {
			return name, nil
		}
		rel := relPath(file, workspace)
		return "edit " + rel, []string{rel}
	case n == "read" || n == "view" || n == "read_file" || n == "view_file" || n == "cat":
		return "read " + relPath(file, workspace), nil
	case n == "grep" || n == "glob" || n == "search" || n == "find" || n == "ls" || n == "list" ||
		strings.Contains(n, "search") || n == "list_directory" || n == "list_dir":
		target := str(args, "pattern", "query", "path", "glob", "dir_path")
		return strings.TrimSpace("search " + truncate(relPath(target, workspace), 120)), nil
	case n == "webfetch" || n == "web_fetch" || n == "fetch":
		return "fetch " + truncate(str(args, "url"), 160), nil
	case n == "todowrite" || n == "update_plan" || n == "todo_write" || n == "todoread":
		return "update the task list", nil
	case n == "task" || n == "agent" || n == "spawn_agent":
		return "delegate " + truncate(str(args, "description", "prompt"), 120), nil
	}
	if s := str(args, "command", "query", "path", "url"); s != "" {
		return fmt.Sprintf("%s %s", name, truncate(s, 120)), nil
	}
	return name, nil
}

func isShellTool(n string) bool {
	switch n {
	case "bash", "shell", "exec_command", "local_shell", "local_shell_call", "run_shell_command", "run_terminal_cmd",
		"terminal", "run_command", "exec", "command":
		return true
	}
	return strings.HasSuffix(n, "_shell") || strings.HasPrefix(n, "shell_")
}

func isEditTool(n string) bool {
	switch n {
	case "edit", "multiedit", "write", "str_replace_editor", "str_replace_based_edit_tool", "notebookedit",
		"create_file", "write_file", "replace", "edit_file", "write_to_file", "replace_file_content", "multi_edit":
		return true
	}
	return strings.Contains(n, "edit") || strings.HasPrefix(n, "write")
}
