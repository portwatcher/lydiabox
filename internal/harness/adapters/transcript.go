package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
)

// Every adapter reads transcripts.
var (
	_ harness.Transcriber = Claude{}
	_ harness.Transcriber = Codex{}
	_ harness.Transcriber = DSH{}
	_ harness.Transcriber = Antigravity{}
	_ harness.Transcriber = OpenCode{}
)

// transcript collects entries and drops empty texts.
type transcript struct{ entries []harness.TranscriptEntry }

func (t *transcript) add(role, text string, at time.Time) {
	if text = strings.TrimSpace(text); text != "" {
		t.entries = append(t.entries, harness.TranscriptEntry{Role: role, Text: text, Time: at})
	}
}

func (t *transcript) tool(name, input string, at time.Time) {
	if name == "" {
		return
	}
	t.entries = append(t.entries, harness.TranscriptEntry{Role: harness.RoleTool, Tool: name, Text: input, Time: at})
}

func (t *transcript) done() harness.Transcript {
	return harness.Transcript{Entries: append([]harness.TranscriptEntry{}, t.entries...)}
}

// maxToolInput bounds the short form of a tool input.
const maxToolInput = 400

// toolInput returns a short form of a tool input: the field that tells the
// most (command, path, pattern, ...) or else the compact JSON. raw is a JSON
// object, a JSON string that holds one, or plain text.
func toolInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.HasPrefix(strings.TrimSpace(s), "{") {
			return toolInput(json.RawMessage(s))
		}
		return shorten(s)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return shorten(string(raw))
	}
	for _, k := range []string{"command", "cmd", "file_path", "filePath", "path", "pattern", "query", "url", "description", "prompt"} {
		v, ok := obj[k]
		if !ok {
			continue
		}
		if json.Unmarshal(v, &s) == nil && s != "" {
			return shorten(s)
		}
		var parts []string
		if json.Unmarshal(v, &parts) == nil && len(parts) > 0 {
			return shorten(strings.Join(parts, " "))
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		return shorten(buf.String())
	}
	return shorten(string(raw))
}

func shorten(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxToolInput {
		return string(r[:maxToolInput-1]) + "…"
	}
	return s
}

// Transcript implements harness.Transcriber.
func (c Claude) Transcript(home, id string) (harness.Transcript, error) {
	rel, err := c.find(home, id)
	if err != nil {
		return harness.Transcript{}, err
	}
	data, err := readHomeFile(home, rel)
	if err != nil {
		return harness.Transcript{}, err
	}
	var t transcript
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil || r.IsSidechain || r.IsMeta || (r.Type != "user" && r.Type != "assistant") {
			return nil
		}
		var m claudeMessage
		if json.Unmarshal(r.Message, &m) != nil {
			return nil
		}
		at := parseTime(r.Timestamp)
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			if r.Type == "user" && !isClaudeNoise(text) {
				t.add(harness.RoleUser, text, at)
			}
			return nil
		}
		_ = json.Unmarshal(m.Content, &blocks)
		for _, b := range blocks {
			switch {
			case b.Type == "text" && r.Type == "user" && !isClaudeNoise(b.Text):
				t.add(harness.RoleUser, b.Text, at)
			case b.Type == "text" && r.Type == "assistant":
				t.add(harness.RoleAssistant, b.Text, at)
			case b.Type == "tool_use":
				t.tool(b.Name, toolInput(b.Input), at)
			}
		}
		return nil
	})
	return t.done(), err
}

// Transcript implements harness.Transcriber.
func (c Codex) Transcript(home, id string) (harness.Transcript, error) {
	rel, err := c.find(home, id)
	if err != nil {
		return harness.Transcript{}, err
	}
	data, err := readHomeFile(home, rel)
	if err != nil {
		return harness.Transcript{}, err
	}
	var t transcript
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		var l codexLine
		if json.Unmarshal(line, &l) != nil || l.RecordType != "" {
			return nil
		}
		item := json.RawMessage(line)
		if len(l.Payload) > 0 {
			if l.Type != "response_item" {
				return nil
			}
			item = l.Payload
		}
		var it struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Input     json.RawMessage `json:"input"`
			Action    json.RawMessage `json:"action"`
		}
		if json.Unmarshal(item, &it) != nil {
			return nil
		}
		at := parseTime(l.Timestamp)
		switch it.Type {
		case "message":
			text := codexText(it.Content)
			switch {
			case it.Role == "user" && !isCodexContext(text):
				t.add(harness.RoleUser, text, at)
			case it.Role == "assistant":
				t.add(harness.RoleAssistant, text, at)
			}
		case "function_call":
			t.tool(it.Name, toolInput(it.Arguments), at)
		case "custom_tool_call":
			t.tool(it.Name, toolInput(it.Input), at)
		case "local_shell_call":
			t.tool("shell", toolInput(it.Action), at)
		}
		return nil
	})
	return t.done(), err
}

// Transcript implements harness.Transcriber.
func (d DSH) Transcript(home, id string) (harness.Transcript, error) {
	rel, err := d.find(home, id)
	if err != nil {
		return harness.Transcript{}, err
	}
	data, err := readDSH(home, rel)
	if err != nil {
		return harness.Transcript{}, err
	}
	type part struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	var t transcript
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		var ev dshEvent
		if json.Unmarshal(line, &ev) != nil {
			return nil
		}
		at := millis(ev.Time)
		switch ev.Type {
		case "user/message":
			var m dshMessage
			if json.Unmarshal(ev.Data, &m) == nil && m.Source.Kind == "user" {
				t.add(harness.RoleUser, m.text(), at)
			}
		case "assistant/message":
			var a struct {
				Message struct {
					Content []part `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(ev.Data, &a) != nil {
				return nil
			}
			for _, p := range a.Message.Content {
				switch p.Type {
				case "text":
					t.add(harness.RoleAssistant, p.Text, at)
				case "tool-call":
					t.tool(p.Name, toolInput(p.Arguments), at)
				}
			}
		}
		return nil
	})
	return t.done(), err
}

// Transcript implements harness.Transcriber. The step payloads have no
// public schema: a user input or planner step shows its longest string,
// and a file step shows the file it names.
func (a Antigravity) Transcript(home, id string) (harness.Transcript, error) {
	if err := checkID(id); err != nil {
		return harness.Transcript{}, err
	}
	steps, err := agySteps(home, id)
	if err != nil {
		if _, serr := statHome(home, path.Join(agyRoot, "conversations", id+".db")); serr != nil {
			return harness.Transcript{}, fmt.Errorf("%w: antigravity session %s", harness.ErrNotFound, id)
		}
		return harness.Transcript{}, err
	}
	var t transcript
	for _, s := range steps {
		switch s.Type {
		case agyStepUserInput:
			t.add(harness.RoleUser, longest(s.Strings), time.Time{})
		case agyStepPlanner:
			t.add(harness.RoleAssistant, longest(s.Strings), time.Time{})
		case agyStepCodeAction, agyStepViewFile:
			name := "edit"
			if s.Type == agyStepViewFile {
				name = "view"
			}
			for _, str := range s.Strings {
				if agyPathRe.MatchString(str) {
					t.tool(name, strings.TrimPrefix(str, "file://"), time.Time{})
					break
				}
			}
		}
	}
	return t.done(), nil
}

// Transcript implements harness.Transcriber.
func (o OpenCode) Transcript(home, id string) (harness.Transcript, error) {
	_, msgs, err := o.load(home, id)
	if err != nil {
		return harness.Transcript{}, err
	}
	var t transcript
	for _, msg := range msgs {
		at := millis(msg.Time.Created)
		role := harness.RoleAssistant
		if msg.Role == "user" {
			role = harness.RoleUser
		}
		for _, p := range msg.Parts {
			switch {
			case p.Type == "text" && !p.Synthetic:
				t.add(role, p.Text, at)
			case p.Type == "tool":
				t.tool(p.Tool, toolInput(p.State.Input), at)
			}
		}
	}
	return t.done(), nil
}
