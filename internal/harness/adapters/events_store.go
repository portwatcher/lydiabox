package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// openCodeEvents reads the events of an opencode session. A tool part holds
// its call and its result, so the tool event carries the result.
func openCodeEvents(home, id string) ([]harness.Event, error) {
	_, msgs, err := OpenCode{}.load(home, id)
	if err != nil {
		return nil, err
	}
	l := newEventLog()
	for _, msg := range msgs {
		at := millis(msg.Time.Created)
		model := strings.Trim(msg.ProviderID+"/"+msg.ModelID, "/")
		for _, p := range msg.Parts {
			switch p.Type {
			case "text":
				if p.Synthetic {
					continue
				}
				kind := harness.EventAssistant
				if msg.Role == "user" {
					kind = harness.EventUser
				}
				l.text(kind, p.ID, p.Text, model, at)
			case "reasoning":
				l.text(harness.EventReasoning, p.ID, p.Text, model, at)
			case "tool":
				ocTool(l, p, at)
			case "step-finish":
				if t := p.Tokens; t != nil {
					in := t.Input + t.Cache.Read + t.Cache.Write
					l.usage(p.ID, model, harness.TurnUsage{InputTokens: in, OutputTokens: t.Output, CachedTokens: t.Cache.Read,
						ReasoningTokens: t.Reasoning, ContextTokens: in, CostUSD: p.Cost}, at)
				}
			}
		}
		if msg.Role != "assistant" {
			continue
		}
		if text := ocError(msg.Error); text != "" {
			l.text(harness.EventError, msg.ID+":error", text, "", millis(msg.Time.Completed))
		}
		if msg.Time.Completed > 0 && msg.Finish != "" && msg.Finish != "tool-calls" {
			l.turnEnd(msg.ID, "", millis(msg.Time.Completed))
		}
	}
	return l.events(), nil
}

func ocTool(l *eventLog, p ocPart, at time.Time) {
	id := p.CallID
	if id == "" {
		id = p.ID
	}
	if s := millis(p.State.Time.Start); !s.IsZero() {
		at = s
	}
	ev := l.call(id, p.Tool, p.State.Input, at)
	if ev == nil {
		return
	}
	switch p.State.Status {
	case "completed", "error":
		out := p.State.Output
		if p.State.Status == "error" {
			b, _ := json.Marshal(p.State.Error)
			out = b
		}
		r := resultOf(id, out, p.State.Status == "error")
		r.Images = findImages(p.State.Attachments, r.Images, 0)
		if p.State.Time.End > p.State.Time.Start && p.State.Time.Start > 0 {
			r.DurationMs = p.State.Time.End - p.State.Time.Start
		}
		ev.Result = r
	default:
		ev.Result = &harness.ToolResult{CallID: id, Status: harness.StatusPending}
	}
}

// ocError returns the text of an opencode message error
// ({"name","data":{"message"}}).
func ocError(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var e struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	if e.Name == "MessageAbortedError" {
		return ""
	}
	if e.Data.Message != "" {
		return e.Data.Message
	}
	return e.Name
}

// antigravityEvents reads the events of an Antigravity conversation. The
// step payloads have no public schema: user and planner steps give text,
// file steps give a read or an edit of the file they name, and the
// checklist of brain/<id>/task.md gives the plan.
func antigravityEvents(home, id string) ([]harness.Event, error) {
	steps, err := agySteps(home, id)
	if err != nil {
		if _, serr := statHome(home, path.Join(agyRoot, "conversations", id+".db")); serr != nil {
			return nil, fmt.Errorf("%w: antigravity session %s", harness.ErrNotFound, id)
		}
		return nil, err
	}
	l := newEventLog()
	for i, s := range steps {
		key := "step" + strconv.Itoa(i)
		switch s.Type {
		case agyStepUserInput:
			l.text(harness.EventUser, key, longest(s.Strings), "", time.Time{})
		case agyStepPlanner:
			l.text(harness.EventAssistant, key, longest(s.Strings), "", time.Time{})
		case agyStepCodeAction, agyStepViewFile:
			name := "edit"
			if s.Type == agyStepViewFile {
				name = "view_file"
			}
			for _, str := range s.Strings {
				if !agyPathRe.MatchString(str) {
					continue
				}
				input, _ := json.Marshal(map[string]string{"path": strings.TrimPrefix(str, "file://")})
				if ev := l.call(key, name, input, time.Time{}); ev != nil {
					ev.Result = &harness.ToolResult{CallID: key, Status: harness.StatusOK}
				}
				break
			}
		}
	}
	task, err := readHomeFile(home, path.Join(agyRoot, "brain", id, "task.md"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if steps := checklist(string(task)); len(steps) > 0 {
		l.put(harness.Event{Key: "plan:task.md", Kind: harness.EventPlan, Plan: steps})
	}
	return l.events(), nil
}

var checkRe = regexp.MustCompile(`^\s*[-*]\s+\[([ xX/~-])\]\s+(.+)$`)

// checklist reads the Markdown task list of a plan file: "[ ]" pending,
// "[/]" in progress, "[x]" done.
func checklist(md string) []harness.PlanStep {
	var out []harness.PlanStep
	for _, line := range strings.Split(md, "\n") {
		m := checkRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		st := harness.StatusPending
		switch m[1] {
		case "x", "X":
			st = harness.StatusCompleted
		case "/", "~", "-":
			st = harness.StatusInProgress
		}
		out = append(out, harness.PlanStep{Text: strings.TrimSpace(m[2]), Status: st})
	}
	return out
}
