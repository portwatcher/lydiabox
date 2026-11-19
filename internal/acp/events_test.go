package acp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// updates applies session/update notifications (the "update" objects) to a
// new bridge and returns its events by key.
func updates(t *testing.T, replay bool, list ...string) map[string]harness.Event {
	t.Helper()
	b := &Bridge{}
	b.ev = newEvents(nil)
	b.replay, b.ev.replay = replay, replay
	clock := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	b.ev.now = func() time.Time { clock = clock.Add(250 * time.Millisecond); return clock }
	for _, u := range list {
		if !json.Valid([]byte(u)) {
			t.Fatalf("invalid update %s", u)
		}
		b.update(json.RawMessage(`{"sessionId":"s1","update":` + u + `}`))
	}
	out := map[string]harness.Event{}
	for _, e := range b.ev.list {
		out[e.Key] = e
	}
	return out
}

func toolEvent(t *testing.T, evs map[string]harness.Event, id string) harness.Event {
	t.Helper()
	e, ok := evs["tool:"+id]
	if !ok || e.Tool == nil || e.Result == nil {
		t.Fatalf("no tool event %s in %+v", id, evs)
	}
	return e
}

// codex-acp 2.7.0 sends the output of a command only in
// _meta.terminal_output_delta, as chunks while it runs.
func TestCodexCommandOutputChunks(t *testing.T) {
	evs := updates(t, false,
		`{"sessionUpdate":"tool_call","toolCallId":"call_x1","kind":"execute","title":"curl -sS https://www.iana.org","status":"in_progress","content":[{"type":"terminal","terminalId":"call_x1"}],"rawInput":{"command":"/bin/bash -lc 'curl -sS https://www.iana.org'","cwd":"/home/lydia"},"_meta":{"terminal_info":{"cwd":"/home/lydia","terminal_id":"call_x1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_x1","_meta":{"terminal_output_delta":{"data":"<!doctype html>\n","terminal_id":"call_x1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_x1","_meta":{"terminal_output_delta":{"data":"<title>IANA</title>\n","terminal_id":"call_x1"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_x1","status":"completed","_meta":{"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"call_x1"}}}`,
	)
	e := toolEvent(t, evs, "call_x1")
	if e.Result.Output != "<!doctype html>\n<title>IANA</title>\n" {
		t.Fatalf("output %q", e.Result.Output)
	}
	if e.Result.Status != harness.StatusOK || e.Tool.Kind != harness.ToolShell {
		t.Fatalf("result %+v tool %+v", e.Result, e.Tool)
	}
	if e.Result.DurationMs != 250 {
		t.Fatalf("duration %d", e.Result.DurationMs)
	}
}

// A command that did not stream sends its whole output once with its end.
func TestCodexCommandOutputAtEnd(t *testing.T) {
	big := strings.Repeat("a", harness.MaxToolOutput+100)
	evs := updates(t, false,
		`{"sessionUpdate":"tool_call","toolCallId":"call_x2","kind":"execute","title":"lbx computer screenshot","status":"in_progress","content":[{"type":"terminal","terminalId":"call_x2"}],"rawInput":{"command":"lbx computer screenshot","cwd":"/home/lydia"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_x2","status":"completed","_meta":{"terminal_output_delta":{"data":"saved /tmp/shot.png\n","terminal_id":"call_x2"},"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"call_x2"}}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call_x3","kind":"execute","title":"yes a","status":"in_progress","rawInput":{"command":"yes a"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_x3","_meta":{"terminal_output_delta":{"data":"`+big+`","terminal_id":"call_x3"}}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_x3","_meta":{"terminal_output_delta":{"data":"more","terminal_id":"call_x3"}}}`,
	)
	e := toolEvent(t, evs, "call_x2")
	if e.Result.Output != "saved /tmp/shot.png\n" || e.Tool.Kind != harness.ToolComputer {
		t.Fatalf("result %+v tool %+v", e.Result, e.Tool)
	}
	if out := toolEvent(t, evs, "call_x3").Result.Output; len(out) > harness.MaxToolOutput+len("…") || strings.Contains(out, "more") {
		t.Fatalf("output not bounded: %d bytes", len(out))
	}
}

// codex-acp names an MCP call "mcp.<server>.<tool>" and wraps the arguments.
func TestCodexMCPSummary(t *testing.T) {
	input := `{"server":"lydiabox","tool":"browser_open","arguments":{"url":"https://www.iana.org"}}`
	evs := updates(t, false,
		`{"sessionUpdate":"tool_call","toolCallId":"call_m1","kind":"execute","title":"mcp.lydiabox.browser_open","status":"in_progress","rawInput":`+input+`,"_meta":{"is_mcp_tool_call":true}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_m1","status":"completed","rawInput":`+input+`,"rawOutput":{"result":{"content":[{"type":"text","text":"Opened https://www.iana.org/"}]},"error":null}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"call_m2","kind":"execute","title":"mcp.lydiabox.computer_screenshot","status":"in_progress","rawInput":{"server":"lydiabox","tool":"computer_screenshot","arguments":{}}}`,
	)
	e := toolEvent(t, evs, "call_m1")
	if e.Tool.Summary != "browser_open https://www.iana.org" || e.Tool.Name != "mcp__lydiabox__browser_open" || e.Tool.Kind != harness.ToolBrowser {
		t.Fatalf("tool %+v", e.Tool)
	}
	if strings.Contains(e.Tool.Input, "server") || !strings.Contains(e.Tool.Input, "https://www.iana.org") {
		t.Fatalf("input %q", e.Tool.Input)
	}
	if !strings.Contains(e.Result.Output, "Opened https://www.iana.org/") {
		t.Fatalf("output %q", e.Result.Output)
	}
	e = toolEvent(t, evs, "call_m2")
	if e.Tool.Summary != "computer_screenshot" || e.Tool.Kind != harness.ToolComputer || e.Tool.Input != "" {
		t.Fatalf("tool %+v", e.Tool)
	}
}

// Claude Code and OpenCode send a screenshot in content and in rawOutput.
func TestResultImagesOnce(t *testing.T) {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"
	img := `{"type":"image","data":"` + png + `","mimeType":"image/png"}`
	evs := updates(t, false,
		`{"sessionUpdate":"tool_call","toolCallId":"toolu_1","kind":"other","title":"mcp__lydiabox__computer_screenshot","status":"pending","rawInput":{}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"toolu_1","status":"completed","content":[{"type":"content","content":`+img+`}],"rawOutput":[`+img+`]}`,
		`{"sessionUpdate":"tool_call","toolCallId":"oc_1","kind":"other","title":"lydiabox_computer_screenshot","status":"pending","rawInput":{}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"oc_1","status":"completed","content":[{"type":"content","content":`+img+`},{"type":"content","content":`+img+`}],"rawOutput":{"output":"","metadata":{"attachments":[{"type":"file","mime":"image/png","url":"data:image/png;base64,`+png+`"}]}}}`,
	)
	for _, id := range []string{"toolu_1", "oc_1"} {
		if imgs := toolEvent(t, evs, id).Result.Images; len(imgs) != 1 || imgs[0] != "data:image/png;base64,"+png {
			t.Fatalf("%s images %v", id, imgs)
		}
	}
}

// A replayed tool call, or one whose first report is its end, has no duration.
func TestReplayNoDuration(t *testing.T) {
	evs := updates(t, true,
		`{"sessionUpdate":"tool_call","toolCallId":"call_r1","kind":"execute","title":"ls","status":"in_progress","rawInput":{"command":"ls"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_r1","status":"completed"}`,
	)
	live := updates(t, false,
		`{"sessionUpdate":"tool_call_update","toolCallId":"call_r2","name":"exec_command","status":"completed","_meta":{"terminal_exit":{"exit_code":0,"signal":null,"terminal_id":"call_r2"}}}`,
	)
	if d := toolEvent(t, live, "call_r2").Result.DurationMs; d != 0 {
		t.Fatalf("first report is the end: duration %d", d)
	}
	if d := toolEvent(t, evs, "call_r1").Result.DurationMs; d != 0 {
		t.Fatalf("duration %d", d)
	}
}
