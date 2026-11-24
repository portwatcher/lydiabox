package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/portwatcher/lydiabox/internal/harness/harnesstest"
)

// emitBridge writes bridge messages as the bridge in the guest terminal does.
func emitBridge(t *testing.T, pty *harnesstest.PTY, msgs ...harness.BridgeMessage) {
	t.Helper()
	var b strings.Builder
	for _, m := range msgs {
		line, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	pty.Emit([]byte(b.String()))
}

func waitInput(t *testing.T, pty *harnesstest.PTY, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(pty.Input()), want) {
		if time.Now().After(deadline) {
			t.Fatalf("terminal input %q does not contain %q", pty.Input(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSessionsACP(t *testing.T) {
	e := newSessionsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, body := e.do(t, http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "codex", "workspaceDir": adapterstest.Workspace,
		"engine": "acp", "model": "deepseek/deepseek-v4-pro", "cols": 100, "rows": 30})
	var s harness.Session
	if resp.StatusCode != 201 || json.Unmarshal(body, &s) != nil || s.Engine != harness.EngineACP || !strings.HasPrefix(s.NativeID, "new-") {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	pty := e.runner.Last()
	cmd := strings.Join(pty.Spec.Command, " ")
	if !strings.HasPrefix(cmd, "lydiabox-guest acp --harness codex --cwd /workspace/lydiabox --model deepseek/deepseek-v4-pro") ||
		!strings.Contains(cmd, "--mcp lydiabox=http://127.0.0.1:8910/mcp") || !strings.Contains(cmd, "--mcp lydiabox-connectors=") {
		t.Fatalf("bridge command %q", cmd)
	}
	for _, h := range []string{"shell", "antigravity"} {
		resp, _ = e.do(t, http.MethodPost, "/api/v1/sessions", map[string]any{"harness": h, "workspaceDir": "/home/agent", "engine": "acp"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s with acp = %d", h, resp.StatusCode)
		}
	}

	c := e.dialEvents(t, ctx, s.ID)
	defer c.CloseNow()
	waitInput(t, pty, `{"type":"sync"}`)
	emitBridge(t, pty,
		harness.BridgeMessage{Type: harness.BridgeReset},
		harness.BridgeMessage{Type: harness.BridgeStatus, Status: &harness.AgentStatus{State: harness.AgentIdle, NativeID: adapterstest.CodexID, Agent: "codex-acp 2.7.0", Mode: "agent-full-access"}},
	)
	m := waitStream(t, ctx, c, "idle", func(m streamMsg) bool { return m.Type == "status" && m.State == harness.StateIdle })
	native := "codex:" + adapterstest.CodexID
	if m.Session != native {
		t.Fatalf("status session %q, want the adopted %q", m.Session, native)
	}
	// The session is listed under its native ID.
	resp, body = e.do(t, http.MethodGet, "/api/v1/sessions?harness=codex", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"id":"`+native+`"`) || !strings.Contains(string(body), `"engine":"acp"`) {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}

	// Prompt, permission, and interrupt go to the bridge as JSON lines.
	path := "/api/v1/sessions/" + url.PathEscape(native)
	if resp, body := e.do(t, http.MethodPost, path+"/input", map[string]any{"text": "Fix the test"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("input = %d %s", resp.StatusCode, body)
	}
	waitInput(t, pty, `{"type":"prompt","text":"Fix the test"}`)
	emitBridge(t, pty, harness.BridgeMessage{Type: harness.BridgeEvent, Event: &harness.Event{Key: "perm1", Kind: harness.EventPermission,
		Permission: &harness.Permission{ID: "perm1", Title: "Run make", Options: []harness.PermissionOption{{ID: "ok", Name: "Allow", Kind: "allow_once"}}}}},
		harness.BridgeMessage{Type: harness.BridgeStatus, Status: &harness.AgentStatus{State: harness.AgentAttention, NativeID: adapterstest.CodexID}})
	waitStream(t, ctx, c, "permission", func(m streamMsg) bool {
		return m.Type == "events" && len(m.Events) == 1 && m.Events[0].Permission != nil && m.Events[0].Permission.Title == "Run make"
	})
	waitStream(t, ctx, c, "attention", func(m streamMsg) bool { return m.Type == "status" && m.State == harness.StateAttention })
	if resp, body := e.do(t, http.MethodPost, path+"/permission", map[string]any{"requestId": "perm1", "optionId": "ok"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("permission = %d %s", resp.StatusCode, body)
	}
	waitInput(t, pty, `{"type":"permission","requestId":"perm1","optionId":"ok"}`)
	if resp, _ := e.do(t, http.MethodPost, path+"/interrupt", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("interrupt = %d", resp.StatusCode)
	}
	waitInput(t, pty, `{"type":"cancel"}`)
	if resp, _ := e.do(t, http.MethodPost, path+"/permission", map[string]any{"requestId": "", "optionId": "ok"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("permission without request = %d", resp.StatusCode)
	}

	// The terminal continues the same native session; the bridge ends.
	resp, body = e.do(t, http.MethodPatch, path, map[string]any{"engine": "pty"})
	var p harness.Session
	if resp.StatusCode != 200 || json.Unmarshal(body, &p) != nil || p.Engine != harness.EnginePTY || p.ID != native {
		t.Fatalf("switch to the terminal = %d %s", resp.StatusCode, body)
	}
	if got := strings.Join(e.runner.Last().Spec.Command, " "); !strings.HasPrefix(got, "codex resume "+adapterstest.CodexID) {
		t.Fatalf("terminal command %q", got)
	}
	if !pty.Exited() {
		t.Fatal("the bridge still runs")
	}
	// A terminal session gets no permission answers.
	if resp, _ := e.do(t, http.MethodPost, path+"/permission", map[string]any{"requestId": "x", "optionId": "ok"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("permission to a terminal = %d", resp.StatusCode)
	}
	resp, _ = e.do(t, http.MethodPatch, path, map[string]any{"engine": "acp", "model": "m"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("model and engine together = %d", resp.StatusCode)
	}
}
