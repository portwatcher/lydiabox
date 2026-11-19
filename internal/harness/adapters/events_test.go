package adapters_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
)

// kinds returns the event kinds in order, as one string.
func kinds(evs []harness.Event) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return strings.Join(out, " ")
}

func find(t *testing.T, evs []harness.Event, key string) harness.Event {
	t.Helper()
	for _, e := range evs {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("no event %q in %s", key, kinds(evs))
	return harness.Event{}
}

func TestEventsPerHarness(t *testing.T) {
	home := adapterstest.Home(t)
	r := adapters.NewEventReader()
	read := func(h, id string) []harness.Event {
		t.Helper()
		evs, err := r.Events(home, h, id)
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}

	t.Run("claude-code", func(t *testing.T) {
		evs := read(harness.ClaudeCode, adapterstest.ClaudeID)
		want := "user assistant usage tool_call tool_result tool_call usage tool_result tool_call usage tool_result assistant usage turn_end user"
		if got := kinds(evs); got != want {
			t.Fatalf("kinds\n got %s\nwant %s", got, want)
		}
		read := find(t, evs, "tool:toolu_01Read")
		if read.Tool.Kind != harness.ToolRead || read.Tool.Path != "/workspace/lydiabox/internal/storage/files/http.go" {
			t.Errorf("read call = %+v", read.Tool)
		}
		edit := find(t, evs, "tool:toolu_02Edit").Tool
		if edit.Kind != harness.ToolEdit || !strings.Contains(edit.Diff, "-func (h *Handler) upload(") || !strings.Contains(edit.Diff, "+func (h *Handler) uploadResumable(") {
			t.Errorf("edit diff = %q", edit.Diff)
		}
		res := find(t, evs, "result:toolu_03Bash").Result
		if res.Status != harness.StatusError || !strings.Contains(res.Output, "got 200, want 308") || res.DurationMs <= 0 {
			t.Errorf("bash result = %+v", res)
		}
		if ok := find(t, evs, "result:toolu_01Read").Result; ok.Status != harness.StatusOK || !strings.Contains(ok.Output, "package files") {
			t.Errorf("read result = %+v", ok)
		}
		u := find(t, evs, "usage:msg_01Upload")
		if u.Usage.ContextTokens != 6200 || u.Model != "claude-sonnet-4-5-20250929" {
			t.Errorf("usage = %+v model %q", u.Usage, u.Model)
		}
		for _, e := range evs {
			if strings.Contains(e.Text, "Sidechain") {
				t.Errorf("sidechain event %+v", e)
			}
		}
	})

	t.Run("codex", func(t *testing.T) {
		evs := read(harness.Codex, adapterstest.CodexID)
		want := "user reasoning tool_call tool_result usage tool_call tool_result tool_call tool_result assistant usage"
		if got := kinds(evs); got != want {
			t.Fatalf("kinds\n got %s\nwant %s", got, want)
		}
		if r := evs[1]; !strings.Contains(r.Text, "Reproducing the flake") {
			t.Errorf("reasoning = %q", r.Text)
		}
		res := find(t, evs, "result:call_Kq1").Result
		if res.Status != harness.StatusError || res.DurationMs != 10800 || !strings.Contains(res.Output, "temp dir removed") {
			t.Errorf("shell result = %+v", res)
		}
		if ok := find(t, evs, "result:call_Kq3").Result; ok.Status != harness.StatusOK {
			t.Errorf("second run = %+v", ok)
		}
		patch := find(t, evs, "tool:call_Pa2").Tool
		if patch.Kind != harness.ToolEdit || patch.Path != "internal/storage/files/service_test.go" || !strings.Contains(patch.Diff, "+\tdir := t.TempDir()") {
			t.Errorf("patch = %+v", patch)
		}
		u := evs[len(evs)-1]
		if u.Usage.ContextWindow != 400000 || u.Usage.ContextTokens != 91344 || u.Model != "deepseek/deepseek-v4-pro" {
			t.Errorf("usage = %+v model %q", u.Usage, u.Model)
		}
	})

	t.Run("codex legacy", func(t *testing.T) {
		evs := read(harness.Codex, adapterstest.CodexLegacyID)
		if !strings.HasPrefix(kinds(evs), "user tool_call") {
			t.Fatalf("kinds %s", kinds(evs))
		}
	})

	t.Run("dsh", func(t *testing.T) {
		evs := read(harness.DSH, adapterstest.DSHID)
		want := "user assistant tool_call usage tool_result tool_call usage tool_result tool_call usage tool_result assistant usage turn_end"
		if got := kinds(evs); got != want {
			t.Fatalf("kinds\n got %s\nwant %s", got, want)
		}
		if res := find(t, evs, "result:tc-3").Result; res.Status != harness.StatusError || !strings.Contains(res.Output, "unreachable code") || res.DurationMs != 4000 {
			t.Errorf("result = %+v", res)
		}
		if edit := find(t, evs, "tool:tc-2").Tool; !strings.Contains(edit.Diff, "+return retry(") {
			t.Errorf("edit diff = %q", edit.Diff)
		}
	})

	t.Run("opencode", func(t *testing.T) {
		evs := read(harness.OpenCode, adapterstest.OpenCodeID)
		want := "user assistant tool_call tool_call usage assistant"
		if got := kinds(evs); got != want {
			t.Fatalf("kinds\n got %s\nwant %s", got, want)
		}
		bash := find(t, evs, "tool:call_b1")
		if bash.Tool.Kind != harness.ToolShell || bash.Result == nil || bash.Result.Status != harness.StatusError ||
			!strings.Contains(bash.Result.Output, "command not found") {
			t.Errorf("bash = %+v %+v", bash.Tool, bash.Result)
		}
		write := find(t, evs, "tool:call_w1")
		if write.Result == nil || write.Result.Status != harness.StatusOK || !strings.Contains(write.Tool.Diff, "+# PTY protocol") {
			t.Errorf("write = %+v %+v", write.Tool, write.Result)
		}
		u := evs[4].Usage
		if u.CostUSD != 0.0123 || u.ContextTokens != 7000 || evs[4].Model != "kimi/kimi-k3" {
			t.Errorf("usage = %+v", u)
		}
	})

	t.Run("antigravity", func(t *testing.T) {
		evs := read(harness.Antigravity, adapterstest.AntigravityID)
		if got := kinds(evs); !strings.HasPrefix(got, "user assistant tool_call tool_call") || !strings.HasSuffix(got, "plan") {
			t.Fatalf("kinds %s", got)
		}
		plan := evs[len(evs)-1].Plan
		if len(plan) != 4 || plan[0].Status != harness.StatusCompleted || plan[2].Status != harness.StatusPending {
			t.Errorf("plan = %+v", plan)
		}
	})

	for _, h := range harness.Harnesses {
		if _, err := r.Events(home, h, "../../etc/passwd"); !errors.Is(err, harness.ErrBadRequest) {
			t.Errorf("%s: path traversal ID: %v", h, err)
		}
		if _, err := r.Events(home, h, "missing-session"); !errors.Is(err, harness.ErrNotFound) {
			t.Errorf("%s: unknown ID: %v", h, err)
		}
	}
}

// TestEventsAppend checks that an appended transcript gives the new events,
// a rewrite starts over, and images and plans are found.
func TestEventsAppend(t *testing.T) {
	home := adapterstest.Home(t)
	dir := filepath.Join(home, ".claude", "projects", "-workspace-lydiabox")
	file := filepath.Join(dir, adapterstest.ClaudeID+".jsonl")
	r := adapters.NewEventReader()
	before, err := r.Events(home, harness.ClaudeCode, adapterstest.ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"assistant","uuid":"x1","timestamp":"2026-11-10T05:15:00Z","message":{"id":"msg_x1","role":"assistant","model":"claude-opus-4-5","content":[{"type":"thinking","thinking":"Plan the work."},{"type":"tool_use","id":"toolu_todo","name":"TodoWrite","input":{"todos":[{"content":"Keep the limit","status":"in_progress","activeForm":"Keeping"},{"content":"Test","status":"pending"}]}},{"type":"tool_use","id":"toolu_shot","name":"mcp__lydiabox__computer_screenshot","input":{}}]}}`,
		`{"type":"user","uuid":"x2","timestamp":"2026-11-10T05:15:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_shot","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}}`,
		`{"type":"assistant","uuid":"x3","timestamp":"2026-11-10T05:15:03Z","message":{"id":"msg_x3","role":"assistant","content":[{"type":"text","text":"partial line without newline"}]}}`,
	}
	if _, err := f.WriteString(strings.Join(lines[:2], "\n") + "\n" + lines[2]); err != nil {
		t.Fatal(err)
	}
	f.Close()
	after, err := r.Events(home, harness.ClaudeCode, adapterstest.ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(after[len(before):]); got != "reasoning tool_call plan tool_call tool_result" {
		t.Fatalf("appended kinds %s", got)
	}
	for i := range before {
		if after[i].Key != before[i].Key {
			t.Fatalf("event %d changed key: %s -> %s", i, before[i].Key, after[i].Key)
		}
	}
	plan := find(t, after, "plan:toolu_todo").Plan
	if len(plan) != 2 || plan[0].Status != harness.StatusInProgress || plan[0].Text != "Keep the limit" {
		t.Errorf("plan = %+v", plan)
	}
	shot := find(t, after, "tool:toolu_shot").Tool
	if shot.Kind != harness.ToolComputer {
		t.Errorf("screenshot kind = %s", shot.Kind)
	}
	if img := find(t, after, "result:toolu_shot").Result.Images; len(img) != 1 || img[0] != "data:image/png;base64,iVBORw0KGgo=" {
		t.Errorf("images = %v", img)
	}

	// A rewrite of the file (not an append) parses it again.
	if err := os.WriteFile(file, []byte(lines[2]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := r.Events(home, harness.ClaudeCode, adapterstest.ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(again); got != "assistant" {
		t.Fatalf("after rewrite: %s", got)
	}
}

func TestEventPage(t *testing.T) {
	big := strings.Repeat("x", harness.MaxEventText)
	var evs []harness.Event
	for i := range 100 {
		evs = append(evs, harness.Event{Key: string(rune('a' + i%26)), Kind: harness.EventAssistant, Text: big})
	}
	page := harness.EventPageOf(evs, 0)
	if page.Total != 100 || page.Next >= 100 || page.Next != len(page.Events) {
		t.Fatalf("page next %d total %d events %d", page.Next, page.Total, len(page.Events))
	}
	rest := harness.EventPageOf(evs, page.Next)
	if rest.Next != 100 || len(rest.Events) != 100-page.Next {
		t.Fatalf("rest next %d events %d", rest.Next, len(rest.Events))
	}
	if end := harness.EventPageOf(evs, 500); len(end.Events) != 0 || end.Next != 100 {
		t.Fatalf("past the end: %+v", end)
	}
}
