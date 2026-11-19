package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
)

func (e *sessionsEnv) dialEvents(t *testing.T, ctx context.Context, id string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/sessions/" + url.PathEscape(id) + "/events"
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{Subprotocols: []string{api.EventsSubprotocol},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type streamMsg struct {
	Type    string          `json:"type"`
	Reset   bool            `json:"reset"`
	Events  []harness.Event `json:"events"`
	State   string          `json:"state"`
	Live    bool            `json:"live"`
	Synced  bool            `json:"synced"`
	Prompt  string          `json:"prompt"`
	Code    string          `json:"code"`
	Session string          `json:"sessionId"`
}

func readStream(t *testing.T, ctx context.Context, c *websocket.Conn) streamMsg {
	t.Helper()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m streamMsg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// waitStream reads messages until cond holds.
func waitStream(t *testing.T, ctx context.Context, c *websocket.Conn, what string, cond func(streamMsg) bool) streamMsg {
	t.Helper()
	for {
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for %s", what)
		}
		if m := readStream(t, ctx, c); cond(m) {
			return m
		}
	}
}

func TestSessionsEventStream(t *testing.T) {
	e := newSessionsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, body := e.do(t, http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "claude-code",
		"workspaceDir": adapterstest.Workspace, "resumeNativeId": adapterstest.ClaudeID, "cols": 100, "rows": 30})
	var s harness.Session
	if resp.StatusCode != 201 || json.Unmarshal(body, &s) != nil {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	pty := e.runner.Last()

	c := e.dialEvents(t, ctx, s.ID)
	defer c.CloseNow()
	first := readStream(t, ctx, c)
	if first.Type != "events" || !first.Reset {
		t.Fatalf("first = %+v", first)
	}
	// The history arrives (in the first reset or the next update).
	var all []harness.Event
	all = append(all, first.Events...)
	waitStream(t, ctx, c, "status", func(m streamMsg) bool {
		all = append(all, m.Events...)
		return m.Type == "status" && m.Synced && m.Live
	})
	if len(all) < 10 || all[0].Kind != harness.EventUser || !strings.HasPrefix(all[0].Text, "Make uploads") {
		t.Fatalf("history = %d events, first %+v", len(all), all[0])
	}

	// An appended line arrives as a new event.
	file := filepath.Join(e.home, ".claude", "projects", "-workspace-lydiabox", adapterstest.ClaudeID+".jsonl")
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"type":"assistant","uuid":"live1","timestamp":"2026-11-10T05:20:00Z","message":{"id":"msg_live","role":"assistant","content":[{"type":"text","text":"Live reply"}]}}` + "\n")
	f.Close()
	waitStream(t, ctx, c, "live event", func(m streamMsg) bool {
		return m.Type == "events" && len(m.Events) > 0 && m.Events[len(m.Events)-1].Text == "Live reply"
	})

	// Output makes the session work; an approval prompt on a quiet screen
	// needs the terminal.
	pty.Emit([]byte("\x1b[?2004h\x1b[1mDo you want to proceed?\x1b[0m\r\n 1. Yes\r\n"))
	m := waitStream(t, ctx, c, "attention", func(m streamMsg) bool { return m.Type == "status" && m.State == harness.StateAttention })
	if m.Prompt != "Do you want to proceed?" {
		t.Fatalf("prompt = %q", m.Prompt)
	}

	// Input is pasted (bracketed paste is on) and submitted.
	resp, body = e.do(t, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(s.ID)+"/input", map[string]any{"text": "Fix it\x1b[201~\r\nnow"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("input = %d %s", resp.StatusCode, body)
	}
	if got := string(pty.Input()); got != "\x1b[200~Fix it[201~\nnow\x1b[201~\r" {
		t.Fatalf("pty input = %q", got)
	}
	resp, _ = e.do(t, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(s.ID)+"/interrupt", nil)
	if resp.StatusCode != http.StatusNoContent || !strings.HasSuffix(string(pty.Input()), "\r\x1b") {
		t.Fatalf("interrupt = %d, input %q", resp.StatusCode, pty.Input())
	}
	resp, _ = e.do(t, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(s.ID)+"/input", map[string]any{"text": "  "})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty input = %d", resp.StatusCode)
	}

	// The end of the terminal makes the session offline.
	pty.Exit(0)
	waitStream(t, ctx, c, "offline", func(m streamMsg) bool { return m.Type == "status" && m.State == harness.StateOffline && !m.Live })
	resp, _ = e.do(t, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(s.ID)+"/input", map[string]any{"text": "again"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("input to an ended session = %d", resp.StatusCode)
	}
}

func TestSessionsEventStreamHistoryAndErrors(t *testing.T) {
	e := newSessionsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A session without a terminal streams its history.
	c := e.dialEvents(t, ctx, "codex:"+adapterstest.CodexID)
	defer c.CloseNow()
	var all []harness.Event
	m := waitStream(t, ctx, c, "synced", func(m streamMsg) bool {
		all = append(all, m.Events...)
		return m.Type == "status" && m.Synced
	})
	if m.Live || m.State != harness.StateOffline || len(all) != 11 {
		t.Fatalf("status %+v, %d events", m, len(all))
	}

	// Shell sessions have no events; an unknown harness is not found.
	for _, id := range []string{"shell:abc", "nope:abc"} {
		c := e.dialEvents(t, ctx, id)
		if m := readStream(t, ctx, c); m.Type != "error" || m.Code != "not_found" {
			t.Fatalf("%s: %+v", id, m)
		}
		c.CloseNow()
	}
}

// A first-run screen (here the Antigravity color scheme) before the first event
// is not "idle", and a prompt sent to it is refused, not lost.
func TestSessionsEventStreamSetupScreen(t *testing.T) {
	e := newSessionsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, body := e.do(t, http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "claude-code",
		"workspaceDir": adapterstest.Workspace, "cols": 100, "rows": 30})
	var s harness.Session
	if resp.StatusCode != 201 || json.Unmarshal(body, &s) != nil {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	pty := e.runner.Last()
	c := e.dialEvents(t, ctx, s.ID)
	defer c.CloseNow()

	// The first Antigravity onboarding step comes before its terms.
	pty.Emit([]byte("\x1b[2J\x1b[H\x1b[1mWelcome to Antigravity CLI!\x1b[0m\r\n\r\nChoose your color scheme:\r\n> terminal\r\n  dark\r\n[Next]\r\n↑/↓ Navigate · enter Confirm"))
	m := waitStream(t, ctx, c, "setup", func(m streamMsg) bool { return m.Type == "status" && m.State == harness.StateSetup })
	if m.Prompt != "Choose your color scheme:" {
		t.Fatalf("prompt = %q", m.Prompt)
	}
	resp, body = e.do(t, http.MethodPost, "/api/v1/sessions/"+url.PathEscape(s.ID)+"/input", map[string]any{"text": "Hello"})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "Terminal view") {
		t.Fatalf("input = %d %s", resp.StatusCode, body)
	}
	if got := pty.Input(); len(got) != 0 {
		t.Fatalf("pty input = %q", got)
	}
}
