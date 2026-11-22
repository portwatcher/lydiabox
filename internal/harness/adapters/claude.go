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
	Type string `json:"type"`
	Text string `json:"text"`
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

func (c Claude) scan(home, rel string) (*harness.SessionMeta, error) {
	data, err := readHomeFile(home, rel)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSuffix(path.Base(rel), ".jsonl")
	s := &harness.SessionMeta{Harness: c.Name(), NativeID: id, Files: []string{hostPath(home, rel)}}
	usage := harness.Usage{}
	seenMsg := map[string]bool{}
	var title, summary string
	var goal firstPrompt
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
		if s.WorkspaceDir == "" && r.Cwd != "" {
			s.WorkspaceDir = r.Cwd
		}
		if ts := parseTime(r.Timestamp); !ts.IsZero() {
			if s.CreatedAt.IsZero() || ts.Before(s.CreatedAt) {
				s.CreatedAt = ts
			}
			if ts.After(s.UpdatedAt) {
				s.UpdatedAt = ts
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
				s.Model = m.Model
			}
			if m.Usage != nil && (m.ID == "" || !seenMsg[m.ID]) {
				seenMsg[m.ID] = true
				usage.InputTokens += m.Usage.InputTokens + m.Usage.CacheCreationTokens + m.Usage.CacheReadTokens
				usage.OutputTokens += m.Usage.OutputTokens
			}
		}
		if r.Type == "user" && !r.IsMeta {
			for _, blk := range claudeBlocks(m.Content) {
				if blk.Type == "text" && !isClaudeNoise(blk.Text) {
					goal.user(blk.Text)
				}
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
	s.Title = firstNonEmptyStr(title, summary, titleFrom(goal))
	if !usage.IsZero() {
		s.Usage = &usage
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
		out = append(out, *s)
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

// LaunchCommand implements harness.Launcher. New sessions get an ID chosen
// up front with --session-id. --dangerously-skip-permissions turns off all
// permission prompts (the VM is the agent's own computer); Claude Code
// refuses it as root, and the guest runs harnesses as UID 1000. The magpie
// wirer sets skipDangerousModePermissionPrompt, so the mode's first-start
// warning does not stop the session.
func (c Claude) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	cmd := []string{"claude", "--dangerously-skip-permissions"}
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
	if o.Native && len(o.MCPServers) > 0 {
		// Guest-native mode: the host does not edit ~/.claude.json (it can
		// hold account data), so the MCP servers come from the command.
		cmd = append(cmd, "--mcp-config", claudeMCPConfig(o.MCPServers))
	}
	return cmd, id
}

// claudeMCPConfig is the --mcp-config JSON of the MCP servers.
func claudeMCPConfig(servers []harness.MCPServer) string {
	type server struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	m := map[string]server{}
	for _, s := range servers {
		m[s.Name] = server{Type: "http", URL: s.URL}
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": m})
	return string(b)
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
