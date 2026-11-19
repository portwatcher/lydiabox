package harness

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Kinds of a session event (docs/api.md section 8, "Session events").
const (
	// EventUser is a prompt of the user.
	EventUser = "user"
	// EventAssistant is text of the agent.
	EventAssistant = "assistant"
	// EventReasoning is the visible reasoning (or its summary) of the agent.
	EventReasoning = "reasoning"
	// EventTool is a tool call. Result is set when the harness stores the
	// call and its result in one record (opencode, antigravity).
	EventTool = "tool_call"
	// EventToolResult is the result of an earlier tool call (Result.CallID).
	EventToolResult = "tool_result"
	// EventPlan is the current plan or to-do list of the agent.
	EventPlan = "plan"
	// EventUsage is the token use of one model call.
	EventUsage = "usage"
	// EventTurnEnd marks the end of an agent turn.
	EventTurnEnd = "turn_end"
	// EventError is an error that the harness recorded.
	EventError = "error"
)

// Generic kinds of a tool, for icons and rendering.
const (
	ToolShell    = "shell"
	ToolRead     = "read"
	ToolEdit     = "edit"
	ToolSearch   = "search"
	ToolWeb      = "web"
	ToolMCP      = "mcp"
	ToolComputer = "computer"
	ToolBrowser  = "browser"
	ToolPlan     = "plan"
	ToolAgent    = "agent"
	ToolOther    = "other"
)

// Statuses of a tool result and of a plan step.
const (
	StatusOK         = "ok"
	StatusError      = "error"
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
)

// Event is one typed item of a session. Events are ordered; Key is stable
// across reads of the same session, so a reader replaces an event with a
// newer version of the same key (a tool call whose result arrived, a
// streamed text that grew).
type Event struct {
	Key    string      `json:"key"`
	Kind   string      `json:"kind"`
	Time   time.Time   `json:"time,omitzero"`
	Text   string      `json:"text,omitempty"`
	Model  string      `json:"model,omitempty"`
	Tool   *ToolCall   `json:"tool,omitempty"`
	Result *ToolResult `json:"result,omitempty"`
	Plan   []PlanStep  `json:"plan,omitempty"`
	Usage  *TurnUsage  `json:"usage,omitempty"`
}

// ToolCall describes the call of one tool.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Summary is a one-line form of the input (command, path, query).
	Summary string `json:"summary,omitempty"`
	// Input is the full input, as indented JSON or text.
	Input string `json:"input,omitempty"`
	// Path is the file that a read or edit names.
	Path string `json:"path,omitempty"`
	// Diff is a unified diff body ("+", "-", " " line prefixes) of an edit.
	Diff string `json:"diff,omitempty"`
}

// ToolResult is the result of one tool call.
type ToolResult struct {
	CallID string `json:"callId"`
	// Status is StatusOK, StatusError, or StatusPending (a call that runs).
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	// Images are data: URLs of images in the result (screenshots).
	Images     []string `json:"images,omitempty"`
	DurationMs int64    `json:"durationMs,omitempty"`
}

// PlanStep is one step of a plan or to-do list.
type PlanStep struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// TurnUsage is the token use of one model call.
type TurnUsage struct {
	InputTokens     int64 `json:"inputTokens,omitempty"`
	OutputTokens    int64 `json:"outputTokens,omitempty"`
	CachedTokens    int64 `json:"cachedTokens,omitempty"`
	ReasoningTokens int64 `json:"reasoningTokens,omitempty"`
	// ContextTokens is the size of the context that the call read.
	ContextTokens int64 `json:"contextTokens,omitempty"`
	// ContextWindow is the context limit of the model, when the harness
	// records it (codex).
	ContextWindow int64   `json:"contextWindow,omitempty"`
	CostUSD       float64 `json:"costUsd,omitempty"`
}

// EventPage is a part of the event list of one session.
type EventPage struct {
	Events []Event `json:"events"`
	// Next is the index after the last event of the page.
	Next int `json:"next"`
	// Total is the number of events of the session.
	Total int `json:"total"`
}

// EventReader reads the typed events of one session. homeDir is the path
// of the guest /home/agent on the machine that runs the reader.
type EventReader interface {
	Events(homeDir, harness, sessionID string) ([]Event, error)
}

// EventSource is implemented by the discoveries that read session events
// (LocalDiscovery, GuestDiscovery). wake allows the call to start or resume
// the machine that holds the files.
type EventSource interface {
	Events(ctx context.Context, tenantID, harness, nativeID string, after int, wake bool) (EventPage, error)
}

// Bounds of the event stream: a page crosses the guest RPC link in one
// frame (rpc.MaxFrame is 4 MiB).
const (
	MaxEventPageBytes = 2 << 20
	MaxEventText      = 32 << 10
	MaxToolInput      = 8 << 10
	MaxToolOutput     = 12 << 10
	MaxToolImage      = 1 << 20
	MaxToolImages     = 2
)

// EventPageOf returns the events from index after, bounded by
// MaxEventPageBytes (at least one event).
func EventPageOf(events []Event, after int) EventPage {
	after = max(0, min(after, len(events)))
	page := EventPage{Events: []Event{}, Next: after, Total: len(events)}
	size := 0
	for i := after; i < len(events); i++ {
		b, err := json.Marshal(events[i])
		if err != nil {
			continue
		}
		if len(page.Events) > 0 && size+len(b) > MaxEventPageBytes {
			break
		}
		size += len(b)
		page.Events = append(page.Events, events[i])
		page.Next = i + 1
	}
	return page
}

// Clip cuts s to n bytes on a rune boundary and marks the cut.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutUTF8(s, n) + "…"
}

// toolKinds maps lower-case tool names of the five harnesses to kinds.
var toolKinds = map[string]string{
	"bash": ToolShell, "shell": ToolShell, "shell_command": ToolShell, "exec_command": ToolShell, "local_shell": ToolShell,
	"run_command": ToolShell, "run_terminal_cmd": ToolShell, "terminal": ToolShell, "bashoutput": ToolShell,
	"killshell": ToolShell, "killbash": ToolShell, "write_stdin": ToolShell, "command": ToolShell,
	"read": ToolRead, "view": ToolRead, "read_file": ToolRead, "view_file": ToolRead, "notebookread": ToolRead,
	"list": ToolRead, "ls": ToolRead, "list_dir": ToolRead, "view_image": ToolRead, "read_many_files": ToolRead,
	"edit": ToolEdit, "multiedit": ToolEdit, "apply_patch": ToolEdit, "patch": ToolEdit, "str_replace": ToolEdit,
	"str_replace_editor": ToolEdit, "str_replace_based_edit_tool": ToolEdit, "write": ToolEdit, "write_file": ToolEdit,
	"create_file": ToolEdit, "edit_file": ToolEdit, "replace": ToolEdit, "notebookedit": ToolEdit,
	"grep": ToolSearch, "glob": ToolSearch, "search": ToolSearch, "find": ToolSearch, "codebase_search": ToolSearch,
	"rg": ToolSearch, "search_file_content": ToolSearch, "codesearch": ToolSearch,
	"webfetch": ToolWeb, "web_fetch": ToolWeb, "websearch": ToolWeb, "web_search": ToolWeb, "fetch": ToolWeb,
	"google_web_search": ToolWeb, "read_url_content": ToolWeb,
	"todowrite": ToolPlan, "todo_write": ToolPlan, "todoread": ToolPlan, "update_plan": ToolPlan, "todo": ToolPlan,
	"plan": ToolPlan, "exit_plan_mode": ToolPlan, "exitplanmode": ToolPlan, "write_todos": ToolPlan,
	"task": ToolAgent, "agent": ToolAgent, "subagent": ToolAgent, "delegate": ToolAgent, "spawn_agent": ToolAgent,
}

// ShellKind refines the kind of a shell call that runs the lbx CLI
// ("lbx browser …", "lbx computer …"): browser and computer use.
func ShellKind(command string) string {
	fields := strings.Fields(command)
	for i, f := range fields {
		if (f == "lbx" || strings.HasSuffix(f, "/lbx")) && i+1 < len(fields) {
			switch fields[i+1] {
			case "browser":
				return ToolBrowser
			case "computer":
				return ToolComputer
			}
		}
	}
	return ToolShell
}

// ToolKind maps a tool name of any harness to a generic kind.
func ToolKind(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "":
		return ToolOther
	case strings.Contains(n, "browser") || strings.Contains(n, "playwright") || strings.Contains(n, "puppeteer"):
		return ToolBrowser
	case strings.Contains(n, "computer") || strings.Contains(n, "screenshot"):
		return ToolComputer
	}
	if k, ok := toolKinds[n]; ok {
		return k
	}
	if strings.HasPrefix(n, "mcp__") || strings.HasPrefix(n, "mcp_") || strings.Contains(n, "__") || strings.Contains(n, "/") {
		return ToolMCP
	}
	return ToolOther
}
