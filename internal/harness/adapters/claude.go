package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// Claude reads Claude Code transcripts:
// ~/.claude/projects/<encoded cwd>/<session uuid>.jsonl, one JSON record
// per line (user, assistant, system, summary, custom-title, ...).
type Claude struct{}

// Name implements harness.HarnessAdapter.
func (Claude) Name() string { return harness.ClaudeCode }

type claudeRecord struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Summary     string          `json:"summary"`
	CustomTitle string          `json:"customTitle"`
	CostUSD     float64         `json:"costUSD"`
	Message     json.RawMessage `json:"message"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		InputTokens         int64 `json:"input_tokens"`
		OutputTokens        int64 `json:"output_tokens"`
		CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// claudeEncode is Claude Code's project directory name for a cwd: every
// character that is not a letter or digit becomes "-".
func claudeEncode(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// blocks decodes message content given as a string or as an array.
func claudeBlocks(raw json.RawMessage) []claudeBlock {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []claudeBlock{{Type: "text", Text: s}}
	}
	var bs []claudeBlock
	_ = json.Unmarshal(raw, &bs)
	return bs
}

// blockText flattens tool_result content.
func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []string
	for _, b := range claudeBlocks(raw) {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// isClaudeNoise reports injected user text (slash command echoes, caveats,
// system reminders) that is not a human prompt.
func isClaudeNoise(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range []string{"<command-name>", "<command-message>", "<local-command-stdout>", "<local-command-stderr>",
		"Caveat: The messages below", "<system-reminder>", "[Request interrupted"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

type claudeScan struct {
	meta    harness.SessionMeta
	builder *builder
}

func (c Claude) scan(home, rel string) (*claudeScan, error) {
	data, err := readHomeFile(home, rel)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSuffix(path.Base(rel), ".jsonl")
	s := &claudeScan{meta: harness.SessionMeta{Harness: c.Name(), NativeID: id, Files: []string{hostPath(home, rel)}}}
	usage := harness.Usage{}
	seenMsg := map[string]bool{}
	var title, summary string
	var b *builder
	var n int
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil {
			return nil
		}
		switch r.Type {
		case "custom-title":
			title = r.CustomTitle
			return nil
		case "summary":
			summary = r.Summary
			return nil
		case "user", "assistant":
		default:
			return nil
		}
		if r.IsSidechain {
			return nil
		}
		if s.meta.WorkspaceDir == "" && r.Cwd != "" {
			s.meta.WorkspaceDir = r.Cwd
		}
		if b == nil {
			b = newBuilder(s.meta.WorkspaceDir)
		}
		if ts := parseTime(r.Timestamp); !ts.IsZero() {
			if s.meta.CreatedAt.IsZero() || ts.Before(s.meta.CreatedAt) {
				s.meta.CreatedAt = ts
			}
			if ts.After(s.meta.UpdatedAt) {
				s.meta.UpdatedAt = ts
			}
		}
		var m claudeMessage
		if json.Unmarshal(r.Message, &m) != nil {
			return nil
		}
		n++
		usage.CostUSD += r.CostUSD
		if r.Type == "assistant" {
			if m.Model != "" && m.Model != "<synthetic>" {
				s.meta.Model = m.Model
			}
			if m.Usage != nil && (m.ID == "" || !seenMsg[m.ID]) {
				seenMsg[m.ID] = true
				usage.InputTokens += m.Usage.InputTokens + m.Usage.CacheCreationTokens + m.Usage.CacheReadTokens
				usage.OutputTokens += m.Usage.OutputTokens
			}
		}
		for _, blk := range claudeBlocks(m.Content) {
			switch {
			case blk.Type == "text" && r.Type == "user":
				if !r.IsMeta && !isClaudeNoise(blk.Text) {
					b.user(blk.Text)
				}
			case blk.Type == "text":
				b.assistant(blk.Text)
			case blk.Type == "tool_use":
				b.tool(blk.ID, blk.Name, jsonArgs(blk.Input))
			case blk.Type == "tool_result":
				b.result(blk.ToolUseID, blk.IsError, blockText(blk.Content))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, harness.ErrNoTranscript
	}
	s.builder = b
	s.meta.Title = firstNonEmptyStr(title, summary, titleFrom(b.goal))
	if !usage.IsZero() {
		s.meta.Usage = &usage
	}
	return s, nil
}

func firstNonEmptyStr(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// DiscoverSessions implements harness.HarnessAdapter.
func (c Claude) DiscoverSessions(home string) ([]harness.SessionMeta, error) {
	files, err := globHome(home, ".claude/projects/*/*.jsonl")
	if err != nil {
		return nil, err
	}
	out := []harness.SessionMeta{}
	for _, rel := range files {
		s, err := c.scan(home, rel)
		if err != nil {
			continue
		}
		out = append(out, s.meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (c Claude) find(home, id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	files, err := globHome(home, ".claude/projects/*/"+id+".jsonl")
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%w: claude-code session %s", harness.ErrNotFound, id)
	}
	return files[0], nil
}

// ParseTranscript implements harness.HarnessAdapter.
func (c Claude) ParseTranscript(home, id string) (*harness.CanonicalTrajectory, error) {
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

// SynthesizeHandoff writes a native seed transcript (the handoff as the
// first user turn plus a synthetic assistant acknowledgement) and resumes
// it with a continuation prompt.
func (c Claude) SynthesizeHandoff(home, workspace string, traj *harness.CanonicalTrajectory) ([]string, string, error) {
	id := newUUID()
	now := time.Now().UTC()
	common := func(typ, uuid, parent string, ts time.Time) map[string]any {
		r := map[string]any{"type": typ, "uuid": uuid, "parentUuid": nil, "sessionId": id, "cwd": workspace,
			"timestamp": ts.Format("2006-01-02T15:04:05.000Z"), "version": "lydiabox-handoff", "isSidechain": false,
			"userType": "external"}
		if parent != "" {
			r["parentUuid"] = parent
		}
		return r
	}
	u1, a1 := newUUID(), newUUID()
	user := common("user", u1, "", now)
	user["message"] = map[string]any{"role": "user", "content": seedText(traj)}
	asst := common("assistant", a1, u1, now.Add(time.Millisecond))
	asst["message"] = map[string]any{"id": "msg_lydiabox_" + strings.ReplaceAll(a1, "-", ""), "type": "message",
		"role": "assistant", "model": "<synthetic>", "content": []map[string]any{{"type": "text", "text": ackText}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}
	title := map[string]any{"type": "custom-title", "customTitle": "Handoff: " + titleFrom(traj.OriginalGoal), "sessionId": id}
	data, err := marshalLines(user, asst, title)
	if err != nil {
		return nil, "", err
	}
	rel := path.Join(".claude/projects", claudeEncode(workspace), id+".jsonl")
	if err := writeHome(home, rel, data); err != nil {
		return nil, "", err
	}
	return []string{"claude", "--resume", id, ContinuePrompt}, id, nil
}

// LaunchCommand implements harness.Launcher. New sessions get an ID chosen
// up front with --session-id.
func (c Claude) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	cmd := []string{"claude"}
	id := o.ResumeNativeID
	if id != "" {
		cmd = append(cmd, "--resume", id)
	} else {
		id = newUUID()
		cmd = append(cmd, "--session-id", id)
	}
	if o.Model != "" {
		cmd = append(cmd, "--model", o.Model)
	}
	return cmd, id
}

// Purge implements harness.Purger.
func (c Claude) Purge(home, id string) error {
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
	// Subagent transcripts live in a directory named after the session.
	err = safefile.RemoveAll(home, strings.TrimSuffix(rel, ".jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
