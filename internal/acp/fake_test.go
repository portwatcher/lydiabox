package acp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// fakeAgent is an in-process ACP agent for tests. It answers initialize,
// session/new, session/load, session/resume, session/set_mode, and
// session/prompt with a scripted turn.
type fakeAgent struct {
	conn *Conn
	// load and resume set the session capabilities.
	load, resume bool
	// authFirst fails the first session/new with "Authentication required".
	authFirst bool

	mu       sync.Mutex
	modes    []string
	authed   []string
	cancel   chan struct{}
	prompts  []string
	closed   bool
	turnHook func(ctx context.Context, sessionID string) string // returns the stop reason
}

// pipePair returns the bridge side and the agent side of a duplex pipe.
func pipePair() (bridgeSide, agentSide io.ReadWriter, closeAll func()) {
	ar, bw := io.Pipe() // bridge writes, agent reads
	br, aw := io.Pipe() // agent writes, bridge reads
	return struct {
			io.Reader
			io.Writer
		}{br, bw}, struct {
			io.Reader
			io.Writer
		}{ar, aw}, func() {
			_ = aw.Close()
			_ = bw.Close()
		}
}

func (f *fakeAgent) start(rw io.ReadWriter) {
	f.cancel = make(chan struct{}, 1)
	f.conn = NewConn(rw, rw, f.handle)
}

func (f *fakeAgent) update(sessionID string, u map[string]any) {
	_ = f.conn.Notify("session/update", map[string]any{"sessionId": sessionID, "update": u})
}

func (f *fakeAgent) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
		ModeID    string `json:"modeId"`
		MethodID  string `json:"methodId"`
		Prompt    []struct {
			Text string `json:"text"`
		} `json:"prompt"`
	}
	_ = json.Unmarshal(params, &p)
	switch method {
	case "initialize":
		caps := map[string]any{"loadSession": f.load, "sessionCapabilities": map[string]any{}}
		if f.resume {
			caps["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
		}
		return map[string]any{"protocolVersion": 1, "agentCapabilities": caps, "agentInfo": map[string]string{"name": "fake", "version": "1.0"},
			"authMethods": []map[string]string{{"id": "api-key", "name": "API key"}}}, nil
	case "authenticate":
		f.mu.Lock()
		f.authed = append(f.authed, p.MethodID)
		f.authFirst = false
		f.mu.Unlock()
		return map[string]any{}, nil
	case "session/new":
		f.mu.Lock()
		need := f.authFirst
		f.mu.Unlock()
		if need {
			return nil, &RPCError{Code: -32000, Message: "Authentication required"}
		}
		return map[string]any{"sessionId": "s1", "modes": map[string]any{"currentModeId": "default",
			"availableModes": []map[string]string{{"id": "default", "name": "Ask"}, {"id": "bypassPermissions", "name": "Bypass"}}}}, nil
	case "session/load":
		f.update(p.SessionID, map[string]any{"sessionUpdate": "user_message_chunk", "content": map[string]string{"type": "text", "text": "Earlier question"}})
		f.update(p.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Earlier answer"}})
		return map[string]any{}, nil
	case "session/resume":
		return map[string]any{}, nil
	case "session/set_mode":
		f.mu.Lock()
		f.modes = append(f.modes, p.ModeID)
		f.mu.Unlock()
		return map[string]any{}, nil
	case "session/close":
		f.mu.Lock()
		f.closed = true
		f.mu.Unlock()
		return map[string]any{}, nil
	case "session/cancel":
		select {
		case f.cancel <- struct{}{}:
		default:
		}
		return nil, nil
	case "session/prompt":
		f.mu.Lock()
		if len(p.Prompt) > 0 {
			f.prompts = append(f.prompts, p.Prompt[0].Text)
		}
		hook := f.turnHook
		f.mu.Unlock()
		reason := "end_turn"
		if hook != nil {
			reason = hook(ctx, p.SessionID)
		}
		return map[string]any{"stopReason": reason, "usage": map[string]int{"inputTokens": 100, "outputTokens": 20, "cachedReadTokens": 50}}, nil
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "no " + method}
}

// scriptedTurn streams a turn with reasoning, text, a tool call that needs
// a permission, a diff, a plan, and context usage.
func (f *fakeAgent) scriptedTurn(ctx context.Context, sid string) string {
	f.update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "Thinking."}})
	f.update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Hello "}})
	f.update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "world"}})
	f.update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Edit main.go", "kind": "edit", "status": "pending",
		"rawInput": map[string]string{"file_path": "/w/main.go"}})
	var out struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	err := f.conn.Call(ctx, "session/request_permission", map[string]any{"sessionId": sid,
		"toolCall": map[string]any{"toolCallId": "t1", "title": "Edit main.go"},
		"options":  []map[string]string{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}, {"optionId": "deny", "name": "Deny", "kind": "reject_once"}}}, &out)
	if err != nil || out.Outcome.Outcome != "selected" || out.Outcome.OptionID != "allow" {
		f.update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "failed"})
		return "cancelled"
	}
	f.update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed",
		"content": []map[string]any{{"type": "diff", "path": "/w/main.go", "oldText": "a\n", "newText": "b\n"},
			{"type": "content", "content": map[string]string{"type": "text", "text": "Edited."}}}})
	f.update(sid, map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{{"content": "Edit", "priority": "high", "status": "completed"},
		{"content": "Test", "priority": "low", "status": "pending"}}})
	f.update(sid, map[string]any{"sessionUpdate": "usage_update", "used": 1200, "size": 200000})
	return "end_turn"
}

// blockingTurn waits for session/cancel.
func (f *fakeAgent) blockingTurn(ctx context.Context, sid string) string {
	f.update(sid, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Working…"}})
	select {
	case <-f.cancel:
		return "cancelled"
	case <-ctx.Done():
		return "cancelled"
	}
}

var errFake = errors.New("fake")
