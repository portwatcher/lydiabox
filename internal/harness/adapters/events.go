package adapters

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// eventLog collects the typed events of one session. It remembers the tool
// calls, so that a result gets its call's duration.
type eventLog struct {
	out   []harness.Event
	index map[string]int // key -> position in out
	calls map[string]time.Time
}

func newEventLog() *eventLog {
	return &eventLog{index: map[string]int{}, calls: map[string]time.Time{}}
}

// put adds e, or replaces the event with the same key.
func (l *eventLog) put(e harness.Event) {
	if i, ok := l.index[e.Key]; ok {
		l.out[i] = e
		return
	}
	l.index[e.Key] = len(l.out)
	l.out = append(l.out, e)
}

func (l *eventLog) events() []harness.Event { return append([]harness.Event{}, l.out...) }

// text adds a user, assistant, reasoning, or error text.
func (l *eventLog) text(kind, key, text, model string, at time.Time) {
	if text = strings.TrimSpace(text); text == "" {
		return
	}
	l.put(harness.Event{Key: key, Kind: kind, Time: at, Text: harness.Clip(text, harness.MaxEventText), Model: model})
}

// call adds a tool call; a plan tool also adds the plan it writes.
func (l *eventLog) call(id, name string, input json.RawMessage, at time.Time) *harness.Event {
	if name == "" {
		return nil
	}
	if id == "" {
		id = "call-" + strconv.Itoa(len(l.out))
	}
	l.calls[id] = at
	tc := toolCallOf(id, name, input)
	l.put(harness.Event{Key: "tool:" + id, Kind: harness.EventTool, Time: at, Tool: tc})
	if tc.Kind == harness.ToolPlan {
		if steps := planSteps(input); len(steps) > 0 {
			l.put(harness.Event{Key: "plan:" + id, Kind: harness.EventPlan, Time: at, Plan: steps})
		}
	}
	return &l.out[l.index["tool:"+id]]
}

// result adds the result of call id. dur is used when it is positive;
// otherwise the time since the call.
func (l *eventLog) result(id string, output json.RawMessage, isErr bool, at time.Time, dur time.Duration) {
	if id == "" {
		return
	}
	r := resultOf(id, output, isErr)
	if dur <= 0 {
		if start, ok := l.calls[id]; ok && !start.IsZero() && !at.IsZero() && at.After(start) {
			dur = at.Sub(start)
		}
	}
	r.DurationMs = dur.Milliseconds()
	l.put(harness.Event{Key: "result:" + id, Kind: harness.EventToolResult, Time: at, Result: r})
}

func (l *eventLog) usage(key, model string, u harness.TurnUsage, at time.Time) {
	if u == (harness.TurnUsage{}) {
		return
	}
	l.put(harness.Event{Key: "usage:" + key, Kind: harness.EventUsage, Time: at, Model: model, Usage: &u})
}

func (l *eventLog) turnEnd(key, text string, at time.Time) {
	l.put(harness.Event{Key: "end:" + key, Kind: harness.EventTurnEnd, Time: at, Text: text})
}

// toolCallOf builds the call of tool name with its raw input (a JSON
// object, a JSON string that holds one, or plain text).
func toolCallOf(id, name string, input json.RawMessage) *harness.ToolCall {
	tc := &harness.ToolCall{ID: id, Name: name, Kind: harness.ToolKind(name), Summary: toolInput(input)}
	obj := inputObject(input)
	tc.Input = harness.Clip(prettyInput(input), harness.MaxToolInput)
	if obj != nil {
		tc.Path = firstString(obj, "file_path", "filePath", "path", "notebook_path", "AbsolutePath", "TargetFile")
	}
	if tc.Kind == harness.ToolEdit {
		tc.Diff = harness.Clip(editDiff(name, input, obj), harness.MaxToolInput)
		if tc.Path == "" {
			tc.Path = patchPath(tc.Diff)
		}
	}
	return tc
}

// inputObject decodes a JSON object input, also when it is a JSON string
// that holds one.
func inputObject(raw json.RawMessage) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		return obj
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && json.Unmarshal([]byte(s), &obj) == nil {
		return obj
	}
	return nil
}

func prettyInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if !strings.HasPrefix(strings.TrimSpace(s), "{") {
			return s
		}
		raw = json.RawMessage(s)
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

func firstString(obj map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		var s string
		if v, ok := obj[k]; ok && json.Unmarshal(v, &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// editDiff returns a diff body for the edit tools of the five harnesses.
func editDiff(name string, raw json.RawMessage, obj map[string]json.RawMessage) string {
	if obj == nil {
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.Contains(s, "*** Begin Patch") {
			return patchBody(s)
		}
		return ""
	}
	if p := firstString(obj, "patch", "input"); strings.Contains(p, "*** Begin Patch") || strings.HasPrefix(p, "--- ") {
		return patchBody(p)
	}
	if v, ok := obj["edits"]; ok {
		var edits []map[string]json.RawMessage
		if json.Unmarshal(v, &edits) == nil {
			var parts []string
			for _, e := range edits {
				parts = append(parts, lineDiff(firstString(e, "old_string", "oldString", "oldText"), firstString(e, "new_string", "newString", "newText")))
			}
			return strings.Join(parts, "\n")
		}
	}
	oldS := firstString(obj, "old_string", "oldString", "oldText", "old_str")
	newS := firstString(obj, "new_string", "newString", "newText", "new_str", "content", "file_text", "CodeContent")
	if oldS == "" && newS == "" {
		return ""
	}
	return lineDiff(oldS, newS)
}

// lineDiff drops the common leading and trailing lines of old and new and
// marks the rest as removed and added.
func lineDiff(oldS, newS string) string {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	}
	a, b := split(oldS), split(newS)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var sb strings.Builder
	ctx := func(lines []string) {
		for _, l := range lines {
			sb.WriteString(" " + l + "\n")
		}
	}
	ctx(a[max(0, pre-2):pre])
	for _, l := range a[pre : len(a)-suf] {
		sb.WriteString("-" + l + "\n")
	}
	for _, l := range b[pre : len(b)-suf] {
		sb.WriteString("+" + l + "\n")
	}
	ctx(a[len(a)-suf : min(len(a), len(a)-suf+2)])
	return strings.TrimSuffix(sb.String(), "\n")
}

// patchBody keeps the file headers and the changed lines of a codex
// apply_patch or unified diff.
func patchBody(p string) string {
	var out []string
	for _, l := range strings.Split(p, "\n") {
		switch {
		case strings.HasPrefix(l, "*** Begin Patch"), strings.HasPrefix(l, "*** End Patch"):
		default:
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// patchPath returns the first file that a patch names.
func patchPath(diff string) string {
	for _, l := range strings.Split(diff, "\n") {
		for _, p := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "+++ b/", "+++ "} {
			if strings.HasPrefix(l, p) {
				return strings.TrimSpace(strings.TrimPrefix(l, p))
			}
		}
	}
	return ""
}

// planSteps reads the steps of a to-do or plan tool input: an array under
// todos, plan, items, or steps, with the text in content, step, text, or
// title and a status.
func planSteps(raw json.RawMessage) []harness.PlanStep {
	obj := inputObject(raw)
	if obj == nil {
		return nil
	}
	for _, k := range []string{"todos", "plan", "items", "steps"} {
		var items []map[string]json.RawMessage
		if json.Unmarshal(obj[k], &items) != nil {
			continue
		}
		var out []harness.PlanStep
		for _, it := range items {
			text := firstString(it, "content", "step", "text", "title", "description")
			if text == "" {
				continue
			}
			out = append(out, harness.PlanStep{Text: text, Status: planStatus(firstString(it, "status", "state"))})
		}
		return out
	}
	return nil
}

func planStatus(s string) string {
	switch strings.ToLower(strings.ReplaceAll(s, "-", "_")) {
	case "completed", "complete", "done", "finished", "success":
		return harness.StatusCompleted
	case "in_progress", "inprogress", "active", "running", "doing":
		return harness.StatusInProgress
	}
	return harness.StatusPending
}

// resultOf builds a tool result from a raw output: plain text, a JSON
// string, an array of content blocks, or an object with output, content,
// or text.
func resultOf(id string, raw json.RawMessage, isErr bool) *harness.ToolResult {
	r := &harness.ToolResult{CallID: id, Status: harness.StatusOK}
	if isErr {
		r.Status = harness.StatusError
	}
	r.Output = harness.Clip(strings.TrimRight(outputText(raw, 0), "\n "), harness.MaxToolOutput)
	r.Images = findImages(raw, nil, 0)
	return r
}

// outputText returns the text of a tool output.
func outputText(raw json.RawMessage, depth int) string {
	if len(raw) == 0 || depth > 4 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t := strings.TrimSpace(s); depth == 0 && (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
			if inner := outputText(json.RawMessage(t), depth+1); inner != "" {
				return inner
			}
		}
		return s
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var parts []string
		for _, el := range arr {
			if t := outputText(el, depth+1); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return string(raw)
	}
	var typ string
	_ = json.Unmarshal(obj["type"], &typ)
	if typ == "image" || typ == "input_image" {
		return ""
	}
	for _, k := range []string{"output", "content", "text", "stdout", "result", "error", "message"} {
		if v, ok := obj[k]; ok {
			if t := outputText(v, depth+1); t != "" {
				return t
			}
		}
	}
	return ""
}

// findImages walks a tool output for images: content blocks of type image
// (Anthropic source.data, MCP data with mimeType), input_image blocks with
// an image_url, and attachments with a data: URL.
func findImages(raw json.RawMessage, out []string, depth int) []string {
	if len(raw) == 0 || depth > 6 || len(out) >= harness.MaxToolImages {
		return out
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) == nil && depth == 0 {
			if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
				return findImages(json.RawMessage(t), out, depth+1)
			}
		}
		return out
	case '[':
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			for _, el := range arr {
				out = findImages(el, out, depth+1)
			}
		}
		return out
	case '{':
	default:
		return out
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return out
	}
	if url := imageURL(obj); url != "" {
		if len(url) <= harness.MaxToolImage {
			out = append(out, url)
		}
		return out
	}
	for _, v := range obj {
		out = findImages(v, out, depth+1)
	}
	return out
}

func imageURL(obj map[string]json.RawMessage) string {
	if u := firstString(obj, "image_url", "url"); strings.HasPrefix(u, "data:image/") {
		return u
	}
	var typ string
	_ = json.Unmarshal(obj["type"], &typ)
	if typ != "image" {
		return ""
	}
	if src, ok := obj["source"]; ok {
		var s map[string]json.RawMessage
		if json.Unmarshal(src, &s) == nil {
			obj = s
		}
	}
	data := firstString(obj, "data")
	mime := firstString(obj, "media_type", "mimeType", "mime_type", "mediaType")
	if data == "" || !strings.HasPrefix(mime, "image/") {
		return ""
	}
	return "data:" + mime + ";base64," + data
}
