package adapters

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// claudeEvents parses a Claude Code transcript.
type claudeEvents struct{ l *eventLog }

func newClaudeEvents() *claudeEvents { return &claudeEvents{l: newEventLog()} }

func (p *claudeEvents) log() *eventLog { return p.l }

func (p *claudeEvents) line(b []byte) {
	var r struct {
		claudeRecord
		IsAPIError bool `json:"isApiErrorMessage"`
	}
	if json.Unmarshal(b, &r) != nil || r.IsSidechain || r.IsMeta || (r.Type != "user" && r.Type != "assistant") {
		return
	}
	var m struct {
		claudeMessage
		StopReason string `json:"stop_reason"`
	}
	if json.Unmarshal(r.Message, &m) != nil {
		return
	}
	at := parseTime(r.Timestamp)
	model := m.Model
	if model == "<synthetic>" {
		model = ""
	}
	key := r.UUID
	if key == "" {
		key = "l" + strconv.Itoa(len(p.l.out))
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		p.userText(key, text, r.Type, at)
		return
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	_ = json.Unmarshal(m.Content, &blocks)
	for i, bl := range blocks {
		k := key + ":" + strconv.Itoa(i)
		switch {
		case bl.Type == "text" && r.Type == "user":
			p.userText(k, bl.Text, r.Type, at)
		case bl.Type == "text" && r.IsAPIError:
			p.l.text(harness.EventError, k, bl.Text, "", at)
		case bl.Type == "text":
			p.l.text(harness.EventAssistant, k, bl.Text, model, at)
		case bl.Type == "thinking":
			p.l.text(harness.EventReasoning, k, bl.Thinking, model, at)
		case bl.Type == "tool_use":
			p.l.call(bl.ID, bl.Name, bl.Input, at)
		case bl.Type == "tool_result":
			p.l.result(bl.ToolUseID, bl.Content, bl.IsError, at, 0)
		}
	}
	if r.Type != "assistant" {
		return
	}
	if u := m.Usage; u != nil && m.ID != "" {
		in := u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
		p.l.usage(m.ID, model, harness.TurnUsage{InputTokens: in, OutputTokens: u.OutputTokens, CachedTokens: u.CacheReadTokens,
			ContextTokens: in, CostUSD: r.CostUSD}, at)
	}
	if m.StopReason == "end_turn" || m.StopReason == "stop_sequence" {
		p.l.turnEnd(key, "", at)
	}
}

func (p *claudeEvents) userText(key, text, typ string, at time.Time) {
	t := strings.TrimSpace(text)
	switch {
	case typ != "user":
		p.l.text(harness.EventAssistant, key, t, "", at)
	case strings.HasPrefix(t, "[Request interrupted"):
		p.l.turnEnd(key, "interrupted", at)
	case !isClaudeNoise(t):
		p.l.text(harness.EventUser, key, t, "", at)
	}
}

// codexEvents parses a Codex rollout (current and 2025 formats).
type codexEvents struct {
	l     *eventLog
	model string
	n     int
}

func newCodexEvents() *codexEvents { return &codexEvents{l: newEventLog()} }

func (p *codexEvents) log() *eventLog { return p.l }

var codexExitRe = regexp.MustCompile(`(?m)^Exit code: (-?\d+)`)

func (p *codexEvents) line(b []byte) {
	p.n++
	key := "l" + strconv.Itoa(p.n)
	var l codexLine
	if json.Unmarshal(b, &l) != nil || l.RecordType != "" {
		return
	}
	at := parseTime(l.Timestamp)
	item := json.RawMessage(b)
	if len(l.Payload) > 0 {
		switch l.Type {
		case "turn_context":
			var tc struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(l.Payload, &tc) == nil && tc.Model != "" {
				p.model = tc.Model
			}
			return
		case "event_msg":
			p.event(key, l.Payload, at)
			return
		case "response_item":
			item = l.Payload
		default:
			return
		}
	}
	var it struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Summary   json.RawMessage `json:"summary"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		ID        string          `json:"id"`
		Arguments json.RawMessage `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		Action    json.RawMessage `json:"action"`
		Output    json.RawMessage `json:"output"`
		Status    string          `json:"status"`
	}
	if json.Unmarshal(item, &it) != nil {
		return
	}
	callID := it.CallID
	if callID == "" {
		callID = it.ID
	}
	switch it.Type {
	case "message":
		text := codexText(it.Content)
		switch {
		case it.Role == "user" && !isCodexContext(text):
			p.l.text(harness.EventUser, key, text, "", at)
		case it.Role == "assistant":
			p.l.text(harness.EventAssistant, key, text, p.model, at)
		}
	case "reasoning":
		p.l.text(harness.EventReasoning, key, codexText(it.Summary), p.model, at)
	case "function_call":
		p.l.call(callID, it.Name, it.Arguments, at)
	case "custom_tool_call":
		p.l.call(callID, it.Name, it.Input, at)
	case "local_shell_call":
		p.l.call(callID, "shell", it.Action, at)
	case "web_search_call":
		if callID == "" {
			callID = key
		}
		p.l.call(callID, "web_search", it.Action, at)
		if it.Status == "completed" {
			p.l.result(callID, nil, false, at, 0)
		}
	case "function_call_output", "custom_tool_call_output":
		p.output(callID, it.Output, at)
	}
}

// output adds a codex tool result: {"output","metadata":{exit_code,
// duration_seconds}} as a JSON string, plain text with an "Exit code:"
// line, or content items.
func (p *codexEvents) output(id string, raw json.RawMessage, at time.Time) {
	var s string
	isErr := false
	var dur time.Duration
	if json.Unmarshal(raw, &s) == nil {
		var o struct {
			Output   json.RawMessage `json:"output"`
			Metadata struct {
				ExitCode *int    `json:"exit_code"`
				Duration float64 `json:"duration_seconds"`
			} `json:"metadata"`
		}
		if json.Unmarshal([]byte(s), &o) == nil && len(o.Output) > 0 {
			raw = o.Output
			isErr = o.Metadata.ExitCode != nil && *o.Metadata.ExitCode != 0
			dur = time.Duration(o.Metadata.Duration * float64(time.Second))
		} else if m := codexExitRe.FindStringSubmatch(s); m != nil {
			isErr = m[1] != "0"
		}
	}
	p.l.result(id, raw, isErr, at, dur)
}

func (p *codexEvents) event(key string, payload json.RawMessage, at time.Time) {
	var ev struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Info    *struct {
			Last struct {
				Input     int64 `json:"input_tokens"`
				Cached    int64 `json:"cached_input_tokens"`
				Output    int64 `json:"output_tokens"`
				Reasoning int64 `json:"reasoning_output_tokens"`
			} `json:"last_token_usage"`
			Window int64 `json:"model_context_window"`
		} `json:"info"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	switch ev.Type {
	case "token_count":
		if ev.Info == nil {
			return
		}
		u := ev.Info.Last
		p.l.usage(key, p.model, harness.TurnUsage{InputTokens: u.Input, OutputTokens: u.Output, CachedTokens: u.Cached,
			ReasoningTokens: u.Reasoning, ContextTokens: u.Input, ContextWindow: ev.Info.Window}, at)
	case "task_complete":
		p.l.turnEnd(key, "", at)
	case "turn_aborted":
		p.l.turnEnd(key, "interrupted", at)
	case "error", "stream_error":
		p.l.text(harness.EventError, key, ev.Message, "", at)
	}
}

// dshEvents parses a dsh session file.
type dshEvents struct{ l *eventLog }

func newDSHEvents() *dshEvents { return &dshEvents{l: newEventLog()} }

func (p *dshEvents) log() *eventLog { return p.l }

func (p *dshEvents) line(b []byte) {
	var ev dshEvent
	if json.Unmarshal(b, &ev) != nil || ev.Type == "session" {
		return
	}
	at := millis(ev.Time)
	key := "s" + strconv.FormatInt(ev.Seq, 10)
	switch ev.Type {
	case "user/message":
		var m dshMessage
		if json.Unmarshal(ev.Data, &m) == nil && m.Source.Kind == "user" {
			p.l.text(harness.EventUser, key, m.text(), "", at)
		}
	case "assistant/message":
		p.assistant(key, ev.Data, at)
	case "tool/result":
		var r struct {
			Message struct {
				ToolCallID string          `json:"toolCallId"`
				IsError    bool            `json:"isError"`
				Content    json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(ev.Data, &r) == nil {
			p.l.result(r.Message.ToolCallID, r.Message.Content, r.Message.IsError, at, 0)
		}
	default:
		if strings.Contains(ev.Type, "error") {
			var e struct {
				Message string `json:"message"`
				Error   struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(ev.Data, &e) == nil {
				p.l.text(harness.EventError, key, e.Message+e.Error.Message, "", at)
			}
		}
	}
}

func (p *dshEvents) assistant(key string, data json.RawMessage, at time.Time) {
	var a struct {
		Message struct {
			Content []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Reasoning string          `json:"reasoning"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
			Source struct {
				Model string `json:"model"`
			} `json:"source"`
		} `json:"message"`
		Usage *struct {
			Input     int64 `json:"inputTokens"`
			Output    int64 `json:"outputTokens"`
			CacheRead int64 `json:"cacheReadTokens"`
			Reasoning int64 `json:"reasoningTokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &a) != nil {
		return
	}
	model := a.Message.Source.Model
	calls := false
	for i, c := range a.Message.Content {
		k := key + ":" + strconv.Itoa(i)
		switch c.Type {
		case "text":
			p.l.text(harness.EventAssistant, k, c.Text, model, at)
		case "reasoning", "thinking":
			p.l.text(harness.EventReasoning, k, c.Text+c.Reasoning, model, at)
		case "tool-call":
			calls = true
			p.l.call(c.ID, c.Name, c.Arguments, at)
		}
	}
	if u := a.Usage; u != nil {
		p.l.usage(key, model, harness.TurnUsage{InputTokens: u.Input, OutputTokens: u.Output, CachedTokens: u.CacheRead,
			ReasoningTokens: u.Reasoning, ContextTokens: u.Input}, at)
	}
	if !calls {
		p.l.turnEnd(key, "", at)
	}
}
