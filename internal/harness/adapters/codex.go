package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// Codex reads OpenAI Codex CLI rollouts:
// ~/.codex/sessions/YYYY/MM/DD/rollout-<local time>-<uuid>.jsonl. Current
// rollouts wrap every item as {timestamp, type, payload}; older ones start
// with a bare {id, timestamp, instructions} line followed by bare items.
type Codex struct{}

// Name implements harness.HarnessAdapter.
func (Codex) Name() string { return harness.Codex }

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	// Old format: the first line carries id and instructions; record_type
	// marks state lines.
	ID         string `json:"id"`
	RecordType string `json:"record_type"`
}

type codexItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Cwd       string          `json:"cwd"`
	Model     string          `json:"model"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Action    json.RawMessage `json:"action"`
	Info      *struct {
		Total struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"total_token_usage"`
	} `json:"info"`
}

var codexFileID = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// isCodexContext reports user items that Codex injects (environment
// context, AGENTS.md instructions) rather than human prompts.
func isCodexContext(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range []string{"<environment_context>", "<user_instructions>", "# AGENTS.md instructions", "<turn_aborted>", "<permissions"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func codexText(raw json.RawMessage) string {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

var codexExitRe = regexp.MustCompile(`(?i)(?:exit code|process exited with code)[: ]+(-?\d+)`)

// codexOutput returns the text of a function_call_output and whether the
// command failed.
func codexOutput(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var o struct {
			Content string `json:"content"`
			Success *bool  `json:"success"`
		}
		_ = json.Unmarshal(raw, &o)
		return o.Content, o.Success != nil && !*o.Success
	}
	var wrapped struct {
		Output   string `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(s), &wrapped) == nil && wrapped.Metadata.ExitCode != nil {
		return wrapped.Output, *wrapped.Metadata.ExitCode != 0
	}
	if m := codexExitRe.FindStringSubmatch(s); m != nil {
		code, _ := strconv.Atoi(m[1])
		return s, code != 0
	}
	return s, false
}

type codexScan struct {
	meta    harness.SessionMeta
	builder *builder
}

func (c Codex) scan(home, rel string) (*codexScan, error) {
	data, err := readHomeFile(home, rel)
	if err != nil {
		return nil, err
	}
	s := &codexScan{meta: harness.SessionMeta{Harness: c.Name(), Files: []string{hostPath(home, rel)}}}
	if m := codexFileID.FindStringSubmatch(rel); m != nil {
		s.meta.NativeID = m[1]
	}
	type entry struct {
		it codexItem
		ts time.Time
	}
	var items []entry
	first := true
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		var l codexLine
		if json.Unmarshal(line, &l) != nil {
			return nil
		}
		ts := parseTime(l.Timestamp)
		if first && l.Type == "" && l.ID != "" {
			s.meta.NativeID = l.ID // old format header
			items = append(items, entry{it: codexItem{Type: "header"}, ts: ts})
			first = false
			return nil
		}
		first = false
		var it codexItem
		switch {
		case l.RecordType != "":
			return nil
		case len(l.Payload) > 0:
			if json.Unmarshal(l.Payload, &it) != nil {
				return nil
			}
			switch l.Type {
			case "session_meta":
				it.Type = "session_meta"
			case "turn_context":
				it.Type = "turn_context"
			case "event_msg":
				if it.Type != "token_count" {
					return nil
				}
			case "response_item":
			default:
				return nil
			}
		default:
			if json.Unmarshal(line, &it) != nil {
				return nil
			}
		}
		items = append(items, entry{it: it, ts: ts})
		return nil
	})
	if err != nil {
		return nil, err
	}
	var b *builder
	var msgs int
	for _, e := range items {
		it, ts := e.it, e.ts
		if !ts.IsZero() {
			if s.meta.CreatedAt.IsZero() || ts.Before(s.meta.CreatedAt) {
				s.meta.CreatedAt = ts
			}
			if ts.After(s.meta.UpdatedAt) {
				s.meta.UpdatedAt = ts
			}
		}
		switch it.Type {
		case "header":
			continue
		case "session_meta":
			if it.ID != "" {
				s.meta.NativeID = it.ID
			}
			if it.Cwd != "" {
				s.meta.WorkspaceDir = it.Cwd
			}
			continue
		case "turn_context":
			if it.Model != "" {
				s.meta.Model = it.Model
			}
			if s.meta.WorkspaceDir == "" && it.Cwd != "" {
				s.meta.WorkspaceDir = it.Cwd
			}
			continue
		case "token_count":
			if it.Info != nil {
				s.meta.Usage = &harness.Usage{InputTokens: it.Info.Total.Input, OutputTokens: it.Info.Total.Output}
			}
			continue
		}
		if b == nil {
			b = newBuilder(s.meta.WorkspaceDir)
		}
		switch it.Type {
		case "message":
			text := codexText(it.Content)
			switch it.Role {
			case "user":
				if !isCodexContext(text) {
					b.user(text)
					msgs++
				}
			case "assistant":
				b.assistant(text)
				msgs++
			}
		case "function_call":
			b.tool(it.CallID, it.Name, jsonArgs(it.Arguments))
		case "custom_tool_call":
			b.tool(it.CallID, it.Name, jsonArgs(it.Input))
		case "local_shell_call":
			b.tool(it.CallID, "local_shell", jsonArgs(it.Action))
		case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
			out, failed := codexOutput(it.Output)
			b.result(it.CallID, failed, out)
		}
	}
	if s.meta.NativeID == "" || b == nil || msgs == 0 {
		return nil, harness.ErrNoTranscript
	}
	s.builder = b
	s.meta.Title = titleFrom(b.goal)
	return s, nil
}

// codexTitles reads thread names from ~/.codex/session_index.jsonl.
func codexTitles(home string) map[string]string {
	out := map[string]string{}
	data, err := readHomeFile(home, ".codex/session_index.jsonl")
	if err != nil {
		return out
	}
	_ = eachLine(bytes.NewReader(data), func(line []byte) error {
		var e struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &e) == nil && e.ID != "" && e.ThreadName != "" {
			out[e.ID] = e.ThreadName
		}
		return nil
	})
	return out
}

func (c Codex) rollouts(home string) ([]string, error) {
	return globHome(home, ".codex/sessions/*/*/*/rollout-*.jsonl")
}

// DiscoverSessions implements harness.HarnessAdapter.
func (c Codex) DiscoverSessions(home string) ([]harness.SessionMeta, error) {
	files, err := c.rollouts(home)
	if err != nil {
		return nil, err
	}
	titles := codexTitles(home)
	out := []harness.SessionMeta{}
	for _, rel := range files {
		s, err := c.scan(home, rel)
		if err != nil {
			continue
		}
		if t := titles[s.meta.NativeID]; t != "" {
			s.meta.Title = t
		}
		out = append(out, s.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (c Codex) find(home, id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	files, err := globHome(home, ".codex/sessions/*/*/*/rollout-*-"+id+".jsonl")
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		return files[0], nil
	}
	// Old rollouts may not carry the ID in the file name.
	all, err := c.rollouts(home)
	if err != nil {
		return "", err
	}
	for _, rel := range all {
		if s, err := c.scan(home, rel); err == nil && s.meta.NativeID == id {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%w: codex session %s", harness.ErrNotFound, id)
}

// ParseTranscript implements harness.HarnessAdapter.
func (c Codex) ParseTranscript(home, id string) (*harness.CanonicalTrajectory, error) {
	rel, err := c.find(home, id)
	if err != nil {
		return nil, err
	}
	s, err := c.scan(home, rel)
	if err != nil {
		return nil, err
	}
	t := s.builder.finish(c.Name(), id, s.meta.Model)
	t.Usage = s.meta.Usage
	return t, nil
}

// SynthesizeHandoff writes a native seed rollout and resumes it with
// `codex resume <id> <prompt>`.
func (c Codex) SynthesizeHandoff(home, workspace string, traj *harness.CanonicalTrajectory) ([]string, string, error) {
	now := time.Now().UTC()
	id := newUUIDv7(now)
	ts := now.Format("2006-01-02T15:04:05.000Z")
	line := func(typ string, payload any) map[string]any {
		return map[string]any{"timestamp": ts, "type": typ, "payload": payload}
	}
	data, err := marshalLines(
		line("session_meta", map[string]any{"id": id, "timestamp": ts, "cwd": workspace, "originator": "lydiabox_handoff",
			"cli_version": "0.0.0", "source": "cli", "model_provider": "lydiabox"}),
		line("response_item", map[string]any{"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": seedText(traj)}}}),
		line("response_item", map[string]any{"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": ackText}}}),
	)
	if err != nil {
		return nil, "", err
	}
	rel := path.Join(".codex/sessions", now.Format("2006/01/02"), "rollout-"+now.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if err := writeHome(home, rel, data); err != nil {
		return nil, "", err
	}
	return []string{"codex", "resume", id, ContinuePrompt}, id, nil
}

// LaunchCommand implements harness.Launcher. Codex picks the ID of a new
// session itself.
func (c Codex) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	cmd := []string{"codex"}
	if o.ResumeNativeID != "" {
		cmd = append(cmd, "resume", o.ResumeNativeID)
	}
	if o.Model != "" {
		cmd = append(cmd, "--model", o.Model)
	}
	return cmd, o.ResumeNativeID
}

// Purge implements harness.Purger. It deletes the rollout and the session
// index entry.
func (c Codex) Purge(home, id string) error {
	rel, err := c.find(home, id)
	if errors.Is(err, harness.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := safefile.RemoveAll(home, rel); err != nil {
		return err
	}
	data, err := readHomeFile(home, ".codex/session_index.jsonl")
	if err != nil {
		return nil
	}
	var kept bytes.Buffer
	_ = eachLine(bytes.NewReader(data), func(line []byte) error {
		var e struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(line, &e) == nil && e.ID == id {
			return nil
		}
		kept.Write(line)
		kept.WriteByte('\n')
		return nil
	})
	if kept.Len() == len(data) {
		return nil
	}
	return safefile.WriteFile(home, ".codex/session_index.jsonl", kept.Bytes(), 0o600)
}
