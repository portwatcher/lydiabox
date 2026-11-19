package acp

import (
	"encoding/json"
	"strings"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// update applies one session/update notification.
func (b *Bridge) update(params json.RawMessage) {
	var n struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Content       json.RawMessage `json:"content"`
		MessageID     string          `json:"messageId"`
		Entries       []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"entries"`
		CurrentModeID     string `json:"currentModeId"`
		AvailableCommands []struct {
			Name string `json:"name"`
		} `json:"availableCommands"`
		Used *int64 `json:"used"`
		Size *int64 `json:"size"`
		Cost *struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
		Severity    string `json:"severity"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if json.Unmarshal(n.Update, &u) != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch u.SessionUpdate {
	case "user_message_chunk":
		// A live prompt is already shown; only a replay carries the prompts.
		if !b.working || b.replay {
			b.ev.chunk(harness.EventUser, u.MessageID, blockText(u.Content), "")
		}
	case "agent_message_chunk":
		b.ev.chunk(harness.EventAssistant, u.MessageID, blockText(u.Content), "")
	case "agent_thought_chunk":
		b.ev.chunk(harness.EventReasoning, u.MessageID, blockText(u.Content), "")
	case "tool_call", "tool_call_update":
		var t toolUpdate
		// codex-acp reports the start of each MCP server as a tool call
		// ("mcp_startup.<name>"); it is not an action of the agent.
		if json.Unmarshal(n.Update, &t) == nil && !strings.HasPrefix(t.ToolCallID, "mcp_startup.") {
			b.ev.tool(t)
		}
	case "plan":
		b.ev.plan(u.Entries)
	case "usage_update":
		if u.Used == nil {
			return
		}
		b.ev.usage("", func(t *harness.TurnUsage) {
			t.ContextTokens = *u.Used
			if u.Size != nil {
				t.ContextWindow = *u.Size
			}
			if u.Cost != nil && strings.EqualFold(u.Cost.Currency, "USD") {
				t.CostUSD = u.Cost.Amount
			}
		})
	case "current_mode_update":
		b.setStatusLocked(func(s *harness.AgentStatus) { s.Mode = u.CurrentModeID })
	case "available_commands_update":
		cmds := make([]string, 0, len(u.AvailableCommands))
		for _, c := range u.AvailableCommands {
			cmds = append(cmds, c.Name)
		}
		b.setStatusLocked(func(s *harness.AgentStatus) { s.Commands = cmds })
	case "notice":
		if u.Severity == "error" || u.Severity == "warning" {
			b.ev.errorText(strings.TrimSpace(u.Title + "\n" + u.Description))
		}
	}
}

// blockText returns the text of a content block (or of an array of them).
func blockText(raw json.RawMessage) string {
	var b contentBlock
	if json.Unmarshal(raw, &b) == nil {
		return b.text()
	}
	var list []contentBlock
	if json.Unmarshal(raw, &list) == nil {
		var parts []string
		for _, x := range list {
			parts = append(parts, x.text())
		}
		return strings.Join(parts, "")
	}
	return ""
}
