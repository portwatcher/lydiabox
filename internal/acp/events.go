package acp

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
)

// events keeps the ordered events of one ACP session and reports every
// added or changed event to emit.
type events struct {
	list  []harness.Event
	index map[string]int
	emit  func(harness.Event)
	now   func() time.Time

	turn    int
	last    string // key of the last added event, to append chunks
	replay  bool   // session/load replays the history: no durations
	calls   map[string]*callState
	nextKey int
}

func newEvents(emit func(harness.Event)) *events {
	return &events{index: map[string]int{}, emit: emit, now: time.Now, calls: map[string]*callState{}}
}

func (l *events) put(e harness.Event) {
	if i, ok := l.index[e.Key]; ok {
		l.list[i] = e
	} else {
		l.index[e.Key] = len(l.list)
		l.list = append(l.list, e)
		l.last = e.Key
	}
	if l.emit != nil {
		l.emit(e)
	}
}

func (l *events) get(key string) (harness.Event, bool) {
	i, ok := l.index[key]
	if !ok {
		return harness.Event{}, false
	}
	return l.list[i], true
}

func (l *events) key(prefix string) string {
	l.nextKey++
	return prefix + strconv.Itoa(l.nextKey)
}

// user adds a prompt and starts a turn.
func (l *events) user(text string) {
	l.turn++
	l.put(harness.Event{Key: l.key("u"), Kind: harness.EventUser, Time: l.now().UTC(), Text: harness.Clip(text, harness.MaxEventText)})
}

// chunk appends text to the last event when it has the same kind (and
// message ID), or adds an event.
func (l *events) chunk(kind, messageID, text, model string) {
	if text == "" {
		return
	}
	if e, ok := l.get(l.last); ok && e.Kind == kind && (messageID == "" || strings.HasSuffix(e.Key, "#"+messageID)) {
		e.Text = harness.Clip(e.Text+text, harness.MaxEventText)
		l.put(e)
		return
	}
	key := l.key(kind[:1])
	if messageID != "" {
		key += "#" + messageID
	}
	if kind == harness.EventUser {
		l.turn++
	}
	l.put(harness.Event{Key: key, Kind: kind, Time: l.now().UTC(), Text: harness.Clip(text, harness.MaxEventText), Model: model})
}

// contentBlock is an ACP content block.
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	URI      string `json:"uri"`
	Name     string `json:"name"`
	Resource *struct {
		URI  string `json:"uri"`
		Text string `json:"text"`
	} `json:"resource"`
}

func (b contentBlock) text() string {
	switch b.Type {
	case "text":
		return b.Text
	case "resource_link":
		return b.URI
	case "resource":
		if b.Resource != nil {
			return b.Resource.Text
		}
	}
	return ""
}

// toolContent is an item of tool call content: content, diff, or terminal.
type toolContent struct {
	Type    string       `json:"type"`
	Content contentBlock `json:"content"`
	Path    string       `json:"path"`
	OldText *string      `json:"oldText"`
	NewText string       `json:"newText"`
}

// toolUpdate holds the fields of tool_call and tool_call_update; absent
// fields are nil.
type toolUpdate struct {
	ToolCallID string                  `json:"toolCallId"`
	Title      *string                 `json:"title"`
	Name       *string                 `json:"name"`
	Kind       *string                 `json:"kind"`
	Status     *string                 `json:"status"`
	Content    []toolContent           `json:"content"`
	HasContent bool                    `json:"-"`
	Locations  []struct{ Path string } `json:"locations"`
	RawInput   json.RawMessage         `json:"rawInput"`
	RawOutput  json.RawMessage         `json:"rawOutput"`
	Meta       *toolMeta               `json:"_meta"`
}

// callState is what an ACP agent says once about a tool call: an update
// leaves out the fields that did not change (claude-agent-acp sends the kind
// and the name only with the first report).
type callState struct {
	start   time.Time
	acpKind string
	named   bool // the name came from `name`, so a title does not replace it
}

// toolMeta holds the command output chunks of codex-acp: a client that
// declares no terminal capability gets the output of a command only in
// `_meta.terminal_output_delta` (Zed: `terminal_output`), not in content
// or rawOutput.
type toolMeta struct {
	TerminalOutput      *terminalChunk `json:"terminal_output"`
	TerminalOutputDelta *terminalChunk `json:"terminal_output_delta"`
}

type terminalChunk struct {
	Data string `json:"data"`
}

// mcpCall is the rawInput of a codex-acp MCP tool call (title
// "mcp.<server>.<tool>").
type mcpCall struct {
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

// codexMCP returns the MCP call of a rawInput that has only the keys
// server, tool, and arguments.
func codexMCP(raw json.RawMessage) (mcpCall, bool) {
	var keys map[string]json.RawMessage
	if json.Unmarshal(raw, &keys) != nil || len(keys) > 3 {
		return mcpCall{}, false
	}
	for k := range keys {
		if k != "server" && k != "tool" && k != "arguments" {
			return mcpCall{}, false
		}
	}
	var c mcpCall
	if json.Unmarshal(raw, &c) != nil || c.Server == "" || c.Tool == "" {
		return mcpCall{}, false
	}
	return c, true
}

// emptyJSON reports an absent, null, or empty JSON value.
func emptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "{}" || s == "[]"
}

// addImage adds a data: URL to the result once, within the limits.
func addImage(r *harness.ToolResult, url string) {
	if len(r.Images) >= harness.MaxToolImages || len(url) > harness.MaxToolImage {
		return
	}
	for _, img := range r.Images {
		if img == url {
			return
		}
	}
	r.Images = append(r.Images, url)
}

// appendOutput appends a command output chunk, up to MaxToolOutput.
func appendOutput(r *harness.ToolResult, data string) {
	if data == "" || len(r.Output) >= harness.MaxToolOutput {
		return
	}
	r.Output = harness.Clip(r.Output+data, harness.MaxToolOutput)
}

// acpKinds maps ACP tool kinds to harness tool kinds.
var acpKinds = map[string]string{
	"read": harness.ToolRead, "edit": harness.ToolEdit, "delete": harness.ToolEdit, "move": harness.ToolEdit,
	"search": harness.ToolSearch, "execute": harness.ToolShell, "fetch": harness.ToolWeb, "switch_mode": harness.ToolPlan,
}

// toolKind combines the ACP kind and the tool name: computer, browser, and
// MCP tools keep their own kind. A name with spaces is a title (codex-acp
// uses the command line), so a known ACP kind decides for it: a "/" or
// "browser" in a command line does not make it an MCP or browser call.
func toolKind(acpKind, name, summary string) string {
	byName := harness.ToolKind(name)
	if _, known := acpKinds[acpKind]; known && strings.ContainsAny(strings.TrimSpace(name), " \t\n") {
		byName = ""
	}
	switch byName {
	case harness.ToolComputer, harness.ToolBrowser, harness.ToolMCP, harness.ToolPlan, harness.ToolAgent:
		return byName
	}
	k, ok := acpKinds[acpKind]
	if !ok {
		k = byName
	}
	if k == harness.ToolShell {
		return harness.ShellKind(summary)
	}
	return k
}

// tool applies a tool_call or tool_call_update.
func (l *events) tool(u toolUpdate) {
	if u.ToolCallID == "" {
		return
	}
	key := "tool:" + u.ToolCallID
	e, seen := l.get(key)
	if !seen {
		e = harness.Event{Key: key, Kind: harness.EventTool, Time: l.now().UTC(),
			Tool: &harness.ToolCall{ID: u.ToolCallID}, Result: &harness.ToolResult{CallID: u.ToolCallID, Status: harness.StatusPending}}
	}
	st := l.calls[u.ToolCallID]
	if st == nil {
		st = &callState{start: e.Time}
		l.calls[u.ToolCallID] = st
	}
	tc := *e.Tool
	r := *e.Result
	if u.Kind != nil && *u.Kind != "" {
		st.acpKind = *u.Kind
	}
	if u.Title != nil && *u.Title != "" {
		if !st.named {
			tc.Name = *u.Title
		}
		if tc.Summary == "" || len(u.RawInput) == 0 {
			tc.Summary = harness.Clip(*u.Title, 400)
		}
	}
	if u.Name != nil && *u.Name != "" {
		tc.Name, st.named = *u.Name, true
	}
	if mcp, ok := codexMCP(u.RawInput); ok {
		// The name has the form of the other harnesses (mcp__server__tool),
		// and the summary and input show the arguments, not the wrapper.
		tc.Name, st.named = "mcp__"+mcp.Server+"__"+mcp.Tool, true
		tc.Summary = mcp.Tool
		if !emptyJSON(mcp.Arguments) {
			if s := adapters.ToolSummary(mcp.Arguments); s != "" {
				tc.Summary = harness.Clip(mcp.Tool+" "+s, 400)
			}
			tc.Input = harness.Clip(adapters.PrettyInput(mcp.Arguments), harness.MaxToolInput)
		}
	} else if !emptyJSON(u.RawInput) {
		if s := adapters.ToolSummary(u.RawInput); s != "" {
			tc.Summary = s
		}
		tc.Input = harness.Clip(adapters.PrettyInput(u.RawInput), harness.MaxToolInput)
		if p := adapters.InputPath(u.RawInput); p != "" {
			tc.Path = p
		}
		if d := adapters.EditDiff(tc.Name, u.RawInput); d != "" && tc.Diff == "" {
			tc.Diff = harness.Clip(d, harness.MaxToolInput)
		}
	}
	if len(u.Locations) > 0 && u.Locations[0].Path != "" {
		tc.Path = u.Locations[0].Path
	}
	// The kind follows the name and the summary: claude-agent-acp first
	// reports a Bash call with the title "Terminal" and an empty input.
	tc.Kind = toolKind(st.acpKind, tc.Name, tc.Summary)
	var out []string
	for _, c := range u.Content {
		switch c.Type {
		case "diff":
			old := ""
			if c.OldText != nil {
				old = *c.OldText
			}
			tc.Diff = harness.Clip(adapters.LineDiff(old, c.NewText), harness.MaxToolInput)
			if tc.Path == "" {
				tc.Path = c.Path
			}
		case "content":
			if c.Content.Type == "image" && c.Content.Data != "" && strings.HasPrefix(c.Content.MimeType, "image/") {
				addImage(&r, "data:"+c.Content.MimeType+";base64,"+c.Content.Data)
			} else if t := c.Content.text(); t != "" {
				out = append(out, t)
			}
		}
	}
	if len(out) > 0 {
		r.Output = harness.Clip(strings.Join(out, "\n"), harness.MaxToolOutput)
	}
	if u.Meta != nil {
		for _, c := range []*terminalChunk{u.Meta.TerminalOutput, u.Meta.TerminalOutputDelta} {
			if c != nil {
				appendOutput(&r, c.Data)
			}
		}
	}
	if len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
		if r.Output == "" {
			r.Output = harness.Clip(strings.TrimSpace(adapters.OutputText(u.RawOutput)), harness.MaxToolOutput)
		}
		for _, img := range adapters.Images(u.RawOutput) {
			addImage(&r, img)
		}
	}
	if u.Status != nil {
		switch *u.Status {
		case "completed":
			r.Status = harness.StatusOK
		case "failed":
			r.Status = harness.StatusError
		default:
			r.Status = harness.StatusPending
		}
		// A replayed call, or a call whose first report is its end, has no
		// duration: its start time is not known.
		if r.Status != harness.StatusPending && !l.replay && seen {
			r.DurationMs = l.now().Sub(st.start).Milliseconds()
		}
	}
	e.Tool, e.Result = &tc, &r
	l.put(e)
}

// plan replaces the plan of the current turn.
func (l *events) plan(entries []struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}) {
	steps := make([]harness.PlanStep, 0, len(entries))
	for _, en := range entries {
		steps = append(steps, harness.PlanStep{Text: en.Content, Status: adapters.PlanStatus(en.Status)})
	}
	l.put(harness.Event{Key: "plan:" + strconv.Itoa(l.turn), Kind: harness.EventPlan, Time: l.now().UTC(), Plan: steps})
}

// usage merges token use into the usage event of the current turn.
func (l *events) usage(model string, apply func(u *harness.TurnUsage)) {
	key := "usage:" + strconv.Itoa(l.turn)
	e, ok := l.get(key)
	u := harness.TurnUsage{}
	if ok && e.Usage != nil {
		u = *e.Usage
	} else {
		e = harness.Event{Key: key, Kind: harness.EventUsage, Time: l.now().UTC()}
	}
	apply(&u)
	if model != "" {
		e.Model = model
	}
	e.Usage = &u
	l.put(e)
}

func (l *events) turnEnd(stopReason string) {
	text := ""
	switch stopReason {
	case "cancelled":
		text = "interrupted"
	case "max_tokens", "max_turn_requests", "refusal":
		text = stopReason
	}
	l.put(harness.Event{Key: l.key("end"), Kind: harness.EventTurnEnd, Time: l.now().UTC(), Text: text})
}

func (l *events) errorText(text string) {
	l.put(harness.Event{Key: l.key("err"), Kind: harness.EventError, Time: l.now().UTC(), Text: harness.Clip(text, harness.MaxEventText)})
}
