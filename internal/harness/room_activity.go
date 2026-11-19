package harness

import (
	"bytes"
	"regexp"
	"strings"
	"time"
)

// activityTail is the output that a room keeps to find an approval prompt.
const activityTail = 8 << 10

// activity is the recent output of a room. It is guarded by Room.mu.
type activity struct {
	last time.Time
	tail []byte
	// paste reports that the program turned on bracketed paste mode.
	paste bool
}

var (
	pasteOn  = []byte("\x1b[?2004h")
	pasteOff = []byte("\x1b[?2004l")
)

func (a *activity) note(chunk []byte, at time.Time) {
	if len(chunk) == 0 {
		return
	}
	// A mode switch can be split across two chunks.
	keep := min(len(a.tail), len(pasteOn)-1)
	look := append(append([]byte(nil), a.tail[len(a.tail)-keep:]...), chunk...)
	on, off := bytes.LastIndex(look, pasteOn), bytes.LastIndex(look, pasteOff)
	if on >= 0 || off >= 0 {
		a.paste = on > off
	}
	a.tail = append(a.tail, chunk...)
	if over := len(a.tail) - activityTail; over > 0 {
		a.tail = append(a.tail[:0:0], a.tail[over:]...)
	}
	if !at.IsZero() {
		a.last = at
	}
}

// Activity is what the output of a terminal shows about its program.
type Activity struct {
	// LastOutput is the time of the last output (zero: none since the
	// host attached).
	LastOutput time.Time
	// BracketedPaste reports that the program reads pasted text as one
	// paste.
	BracketedPaste bool
	// Prompt is the line of an approval prompt that the screen shows.
	Prompt string
}

// Activity returns the recent output state of the terminal.
func (r *Room) Activity() Activity {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Activity{LastOutput: r.act.last, BracketedPaste: r.act.paste, Prompt: approvalPrompt(r.act.tail)}
}

var (
	// ansiRe matches escape sequences: CSI, OSC, and two-byte escapes.
	ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78DEHMNOZc]`)
	// approvalRe matches the approval prompts of the five harnesses and
	// of common confirmations.
	approvalRe = regexp.MustCompile(`(?i)(do you want to (?:proceed|make this edit|create|run|allow|apply)[^\n]*\?|would you like to (?:run|make|apply|allow)[^\n]*\?|allow (?:once|always|command|this (?:command|action|edit))|permission required|requires? (?:your )?approval|approve this[^\n]*|\(y/n\)|\[y/n\]|yes, and don't ask again)`)
)

// screenText strips the escape sequences of terminal output. Cursor moves
// become spaces, colors disappear.
func screenText(b []byte) string {
	s := ansiRe.ReplaceAllStringFunc(string(b), func(seq string) string {
		if strings.HasSuffix(seq, "m") {
			return ""
		}
		return " "
	})
	return strings.ReplaceAll(s, "\r", "\n")
}

// approvalPrompt returns the approval prompt in the last screenful of
// output, or "".
func approvalPrompt(tail []byte) string {
	text := screenText(tail)
	if r := []rune(text); len(r) > 2000 {
		text = string(r[len(r)-2000:])
	}
	loc := approvalRe.FindAllStringIndex(text, -1)
	if len(loc) == 0 {
		return ""
	}
	m := loc[len(loc)-1]
	start := strings.LastIndexByte(text[:m[0]], '\n') + 1
	end := len(text)
	if i := strings.IndexByte(text[m[1]:], '\n'); i >= 0 {
		end = m[1] + i
	}
	return strings.Join(strings.Fields(text[start:end]), " ")
}
