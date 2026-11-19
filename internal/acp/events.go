package acp

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
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
	starts  map[string]time.Time
	nextKey int
}

func newEvents(emit func(harness.Event)) *events {
	return &events{index: map[string]int{}, emit: emit, now: time.Now, starts: map[string]time.Time{}}
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
}

// acpKinds maps ACP tool kinds to harness tool kinds.
var acpKinds = map[string]string{
	"read": harness.ToolRead, "edit": harness.ToolEdit, "delete": harness.ToolEdit, "move": harness.ToolEdit,
	"search": harness.ToolSearch, "execute": harness.ToolShell, "fetch": harness.ToolWeb, "switch_mode": harness.ToolPlan,
}

// toolKind combines the ACP kind and the tool name: computer, browser, and
// MCP tools keep their own kind.
func toolKind(acpKind, name, summary string) string {
	byName := harness.ToolKind(name)
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
	e, ok := l.get(key)
	if !ok {
		e = harness.Event{Key: key, Kind: harness.EventTool, Time: l.now().UTC(),
			Tool: &harness.ToolCall{ID: u.ToolCallID}, Result: &harness.ToolResult{CallID: u.ToolCallID, Status: harness.StatusPending}}
		l.starts[u.ToolCallID] = e.Time
	}
	tc := *e.Tool
	r := *e.Result
	acpKind := ""
	if u.Kind != nil {
		acpKind = *u.Kind
	}
	if u.Title != nil && *u.Title != "" {
		tc.Name = *u.Title
		if tc.Summary == "" || len(u.RawInput) == 0 {
			tc.Summary = harness.Clip(*u.Title, 400)
		}
	}
	if u.Name != nil && *u.Name != "" {
		tc.Name = *u.Name
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "null" && string(u.RawInput) != "{}" {
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
	if acpKind != "" || tc.Kind == "" {
		tc.Kind = toolKind(acpKind, tc.Name, tc.Summary)
	}
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
			if c.Content.Type == "image" && c.Content.Data != "" && strings.HasPrefix(c.Content.MimeType, "image/") &&
				len(r.Images) < harness.MaxToolImages {
				if url := "data:" + c.Content.MimeType + ";base64," + c.Content.Data; len(url) <= harness.MaxToolImage {
					r.Images = append(r.Images, url)
				}
			} else if t := c.Content.text(); t != "" {
				out = append(out, t)
			}
		}
	}
	if len(out) > 0 {
		r.Output = harness.Clip(strings.Join(out, "\n"), harness.MaxToolOutput)
	}
	if len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
		if r.Output == "" {
			r.Output = harness.Clip(strings.TrimSpace(adapters.OutputText(u.RawOutput)), harness.MaxToolOutput)
		}
		for _, img := range adapters.Images(u.RawOutput) {
			if len(r.Images) < harness.MaxToolImages {
				r.Images = append(r.Images, img)
			}
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
		if r.Status != harness.StatusPending {
			if start, ok := l.starts[u.ToolCallID]; ok {
				r.DurationMs = l.now().Sub(start).Milliseconds()
			}
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
