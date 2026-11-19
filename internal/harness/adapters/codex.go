package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
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
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Cwd     string          `json:"cwd"`
	Model   string          `json:"model"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Info    *struct {
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

func (c Codex) scan(home, rel string) (*harness.SessionMeta, error) {
	data, err := readHomeFile(home, rel)
	if err != nil {
		return nil, err
	}
	s := &harness.SessionMeta{Harness: c.Name(), Files: []string{hostPath(home, rel)}}
	if m := codexFileID.FindStringSubmatch(rel); m != nil {
		s.NativeID = m[1]
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
			s.NativeID = l.ID // old format header
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
	var goal firstPrompt
	var msgs int
	for _, e := range items {
		it, ts := e.it, e.ts
		if !ts.IsZero() {
			if s.CreatedAt.IsZero() || ts.Before(s.CreatedAt) {
				s.CreatedAt = ts
			}
			if ts.After(s.UpdatedAt) {
				s.UpdatedAt = ts
			}
		}
		switch it.Type {
		case "header":
			continue
		case "session_meta":
			if it.ID != "" {
				s.NativeID = it.ID
			}
			if it.Cwd != "" {
				s.WorkspaceDir = it.Cwd
			}
			continue
		case "turn_context":
			if it.Model != "" {
				s.Model = it.Model
			}
			if s.WorkspaceDir == "" && it.Cwd != "" {
				s.WorkspaceDir = it.Cwd
			}
			continue
		case "token_count":
			if it.Info != nil {
				s.Usage = &harness.Usage{InputTokens: it.Info.Total.Input, OutputTokens: it.Info.Total.Output}
			}
			continue
		}
		if it.Type != "message" {
			continue
		}
		switch it.Role {
		case "user":
			if text := codexText(it.Content); !isCodexContext(text) {
				goal.user(text)
				msgs++
			}
		case "assistant":
			msgs++
		}
	}
	if s.NativeID == "" || msgs == 0 {
		return nil, harness.ErrNoTranscript
	}
	s.Title = titleFrom(goal)
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
		if t := titles[s.NativeID]; t != "" {
			s.Title = t
		}
		out = append(out, *s)
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
		if s, err := c.scan(home, rel); err == nil && s.NativeID == id {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%w: codex session %s", harness.ErrNotFound, id)
}

// codexFullAccess runs commands without the Codex sandbox and never asks
// for approval; MCP tool calls are approved too (Codex 0.169.0
// mcp_permission_prompt_is_auto_approved: approval "never" with the sandbox
// off). The VM is the agent's own computer and its security boundary.
const codexFullAccess = "--dangerously-bypass-approvals-and-sandbox"

// LaunchCommand implements harness.Launcher. Codex picks the ID of a new
// session itself.
func (c Codex) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	cmd := []string{"codex"}
	if o.ResumeNativeID != "" {
		cmd = append(cmd, "resume", o.ResumeNativeID)
	}
	cmd = append(cmd, codexFullAccess)
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
