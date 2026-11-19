package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// host is the host side of a bridge under test.
type host struct {
	t    *testing.T
	in   *io.PipeWriter
	msgs chan harness.BridgeMessage
	evs  map[string]harness.Event
	st   harness.AgentStatus
}

func startBridge(t *testing.T, f *fakeAgent, opts Options) (*host, chan error) {
	t.Helper()
	bridgeSide, agentSide, closeAgent := pipePair()
	f.start(agentSide)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	opts.In, opts.Out, opts.Agent = inR, outW, bridgeSide
	if opts.Harness == "" {
		opts.Harness = harness.ClaudeCode
	}
	if opts.Cwd == "" {
		opts.Cwd = "/w"
	}
	opts.InitTimeout = 5 * time.Second
	h := &host{t: t, in: inW, msgs: make(chan harness.BridgeMessage, 256), evs: map[string]harness.Event{}}
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 8<<20)
		for sc.Scan() {
			var m harness.BridgeMessage
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				h.msgs <- m
			}
		}
		close(h.msgs)
	}()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- NewBridge(opts).Run(ctx)
		_ = outW.Close()
	}()
	t.Cleanup(func() {
		cancel()
		_ = inW.Close()
		closeAgent()
	})
	return h, done
}

func (h *host) send(c harness.BridgeCommand) {
	b, _ := json.Marshal(c)
	if _, err := h.in.Write(append(b, '\n')); err != nil {
		h.t.Fatal(err)
	}
}

// until reads messages until cond holds.
func (h *host) until(what string, cond func(harness.BridgeMessage) bool) harness.BridgeMessage {
	h.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-h.msgs:
			if !ok {
				h.t.Fatalf("bridge output ended before %s", what)
			}
			switch m.Type {
			case harness.BridgeReset:
				h.evs = map[string]harness.Event{}
			case harness.BridgeEvent:
				h.evs[m.Event.Key] = *m.Event
			case harness.BridgeStatus:
				h.st = *m.Status
			}
			if cond(m) {
				return m
			}
		case <-timeout:
			h.t.Fatalf("timed out waiting for %s (status %+v)", what, h.st)
		}
	}
}

func (h *host) state(s string) {
	h.t.Helper()
	h.until("state "+s, func(m harness.BridgeMessage) bool { return m.Type == harness.BridgeStatus && m.Status.State == s })
}

func (h *host) find(kind string) []harness.Event {
	var out []harness.Event
	for _, e := range h.evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestBridgeTurn(t *testing.T) {
	f := &fakeAgent{authFirst: true}
	f.turnHook = f.scriptedTurn
	h, _ := startBridge(t, f, Options{MCP: []MCPServer{{Name: "lydiabox", URL: "http://127.0.0.1:8910/mcp"}}})
	h.state(harness.AgentIdle)
	if h.st.NativeID != "s1" || h.st.Mode != "bypassPermissions" || h.st.Agent != "fake 1.0" {
		t.Fatalf("status = %+v", h.st)
	}
	if len(f.authed) != 1 || f.authed[0] != "api-key" || len(f.modes) != 1 || f.modes[0] != "bypassPermissions" {
		t.Fatalf("auth %v, modes %v", f.authed, f.modes)
	}

	h.send(harness.BridgeCommand{Type: harness.BridgePrompt, Text: "Fix main.go"})
	perm := h.until("permission", func(m harness.BridgeMessage) bool {
		return m.Type == harness.BridgeEvent && m.Event.Kind == harness.EventPermission
	}).Event.Permission
	h.state(harness.AgentAttention)
	if perm.Title != "Edit main.go" || len(perm.Options) != 2 || perm.Outcome != "" {
		t.Fatalf("permission %+v, state %s", perm, h.st.State)
	}
	h.send(harness.BridgeCommand{Type: harness.BridgePermission, RequestID: perm.ID, OptionID: "allow"})
	h.until("turn end", func(m harness.BridgeMessage) bool {
		return m.Type == harness.BridgeEvent && m.Event.Kind == harness.EventTurnEnd
	})
	h.state(harness.AgentIdle)

	if u := h.find(harness.EventUser); len(u) != 1 || u[0].Text != "Fix main.go" {
		t.Errorf("user events %+v", u)
	}
	if a := h.find(harness.EventAssistant); len(a) != 1 || a[0].Text != "Hello world" {
		t.Errorf("assistant events %+v", a)
	}
	if r := h.find(harness.EventReasoning); len(r) != 1 || r[0].Text != "Thinking." {
		t.Errorf("reasoning %+v", r)
	}
	tool := h.evs["tool:t1"]
	if tool.Tool == nil || tool.Tool.Kind != harness.ToolEdit || tool.Tool.Path != "/w/main.go" || !strings.Contains(tool.Tool.Diff, "-a") ||
		tool.Result.Status != harness.StatusOK || tool.Result.Output != "Edited." {
		t.Errorf("tool %+v %+v", tool.Tool, tool.Result)
	}
	if p := h.evs[perm.ID].Permission; p.Outcome != "allow" {
		t.Errorf("answered permission %+v", p)
	}
	if pl := h.find(harness.EventPlan); len(pl) != 1 || len(pl[0].Plan) != 2 || pl[0].Plan[0].Status != harness.StatusCompleted {
		t.Errorf("plan %+v", pl)
	}
	u := h.find(harness.EventUsage)
	if len(u) != 1 || u[0].Usage.ContextTokens != 1200 || u[0].Usage.ContextWindow != 200000 || u[0].Usage.InputTokens != 150 || u[0].Usage.OutputTokens != 20 {
		t.Errorf("usage %+v", u)
	}

	// sync sends everything again after a reset.
	n := len(h.evs)
	h.send(harness.BridgeCommand{Type: harness.BridgeSync})
	h.until("reset", func(m harness.BridgeMessage) bool { return m.Type == harness.BridgeReset })
	h.until("resent status", func(m harness.BridgeMessage) bool { return m.Type == harness.BridgeStatus })
	if len(h.evs) != n {
		t.Fatalf("after sync %d events, want %d", len(h.evs), n)
	}
}

func TestBridgeCancel(t *testing.T) {
	f := &fakeAgent{}
	f.turnHook = f.blockingTurn
	h, _ := startBridge(t, f, Options{})
	h.state(harness.AgentIdle)
	h.send(harness.BridgeCommand{Type: harness.BridgePrompt, Text: "Long task"})
	h.send(harness.BridgeCommand{Type: harness.BridgePrompt, Text: "Queued task"})
	h.until("working", func(m harness.BridgeMessage) bool {
		return m.Type == harness.BridgeEvent && m.Event.Text == "Working…"
	})
	h.send(harness.BridgeCommand{Type: harness.BridgeCancel})
	end := h.until("turn end", func(m harness.BridgeMessage) bool {
		return m.Type == harness.BridgeEvent && m.Event.Kind == harness.EventTurnEnd
	})
	if end.Event.Text != "interrupted" {
		t.Fatalf("turn end %+v", end.Event)
	}
	h.state(harness.AgentIdle)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) != 1 {
		t.Fatalf("prompts sent %v: the queued prompt must be dropped", f.prompts)
	}
}

func TestBridgeLoadAndResume(t *testing.T) {
	// session/load replays the history.
	f := &fakeAgent{load: true}
	h, _ := startBridge(t, f, Options{Resume: "s0"})
	h.state(harness.AgentIdle)
	if h.st.NativeID != "s0" {
		t.Fatalf("native ID %q", h.st.NativeID)
	}
	if u := h.find(harness.EventUser); len(u) != 1 || u[0].Text != "Earlier question" {
		t.Fatalf("replayed user %+v", u)
	}

	// session/resume without load: the transcript gives the history.
	g := &fakeAgent{resume: true}
	hist := historyFunc(func(_, _, id string) ([]harness.Event, error) {
		return []harness.Event{{Key: "t1", Kind: harness.EventUser, Text: "From transcript " + id}}, nil
	})
	h2, _ := startBridge(t, g, Options{Resume: "s9", History: hist, Harness: harness.DSH})
	h2.state(harness.AgentIdle)
	if u := h2.find(harness.EventUser); len(u) != 1 || u[0].Text != "From transcript s9" {
		t.Fatalf("history %+v", u)
	}

	// An agent with neither cannot continue a session.
	k := &fakeAgent{}
	_, done := startBridge(t, k, Options{Resume: "s9"})
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cannot continue") {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not end")
	}
}

func TestBridgeAgentExit(t *testing.T) {
	f := &fakeAgent{}
	bridgeSide, agentSide, closeAgent := pipePair()
	f.start(agentSide)
	inR, inW := io.Pipe()
	defer inW.Close()
	var out strings.Builder
	done := make(chan error, 1)
	go func() {
		done <- NewBridge(Options{Harness: harness.Codex, Cwd: "/w", In: inR, Out: &syncWriter{w: &out}, Agent: bridgeSide, InitTimeout: 5 * time.Second}).Run(context.Background())
	}()
	time.Sleep(100 * time.Millisecond)
	closeAgent()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "the agent ended") {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not end after the agent")
	}
}

type historyFunc func(home, h, id string) ([]harness.Event, error)

func (f historyFunc) Events(home, h, id string) ([]harness.Event, error) { return f(home, h, id) }

type syncWriter struct {
	w io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) { return s.w.Write(p) }

func TestToolKindMapping(t *testing.T) {
	for _, tc := range []struct{ acp, name, summary, want string }{
		{"execute", "Run tests", "go test ./...", harness.ToolShell},
		{"execute", "Run lbx", "lbx browser open https://x", harness.ToolBrowser},
		{"read", "Read file", "", harness.ToolRead},
		{"delete", "Delete x", "", harness.ToolEdit},
		{"fetch", "Fetch", "", harness.ToolWeb},
		{"other", "mcp__lydiabox__computer_screenshot", "", harness.ToolComputer},
		{"", "mcp__github__create_issue", "", harness.ToolMCP},
		{"think", "Think", "", harness.ToolOther},
	} {
		if got := toolKind(tc.acp, tc.name, tc.summary); got != tc.want {
			t.Errorf("toolKind(%q, %q) = %s, want %s", tc.acp, tc.name, got, tc.want)
		}
	}
}

func TestAgentFor(t *testing.T) {
	for _, h := range harness.Harnesses {
		a, err := AgentFor(h, "m1")
		if err != nil || len(a.Command) == 0 {
			t.Fatalf("%s: %v %+v", h, err, a)
		}
	}
	if a, _ := AgentFor(harness.ClaudeCode, "opus"); a.Env["ANTHROPIC_MODEL"] != "opus" {
		t.Errorf("claude env %v", a.Env)
	}
	if a, _ := AgentFor(harness.DSH, "deepseek-v4"); !strings.Contains(strings.Join(a.Command, " "), "DSH_MODEL=deepseek-v4") {
		t.Errorf("dsh command %v", a.Command)
	}
	if _, err := AgentFor(harness.Shell, ""); err == nil {
		t.Error("shell has no ACP agent")
	}
}

var _ = errFake
