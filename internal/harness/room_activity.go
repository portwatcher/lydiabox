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
	// fresh is the output after the last input. A setup screen counts only
	// when this output shows it: the text of a screen that the user closed
	// stays in tail, but the program writes other output after the input.
	fresh []byte
	// setupAtInput is the setup line at the last input; it stays until the
	// program writes again.
	setupAtInput string
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
	a.tail = keepTail(append(a.tail, chunk...))
	a.fresh = keepTail(append(a.fresh, chunk...))
	if !at.IsZero() {
		a.last = at
	}
}

func keepTail(b []byte) []byte {
	if over := len(b) - activityTail; over > 0 {
		return append(b[:0:0], b[over:]...)
	}
	return b
}

// input records that a client wrote to the terminal.
func (a *activity) input() {
	a.setupAtInput = a.setup()
	a.fresh = a.fresh[:0]
}

// setup returns the title of the first-run setup screen that the terminal
// shows, or "".
func (a *activity) setup() string {
	if len(a.fresh) == 0 {
		return a.setupAtInput
	}
	if title := lastMatchTitle(setupRe, a.fresh); title != "" {
		return title
	}
	return lastMatchTitle(setupStepRe, a.fresh)
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
	// Setup is the line of a first-run screen (terms, folder trust, sign-in
	// method) that waits for the user. Input sent to the program before the
	// user finishes it is lost.
	Setup string
}

// Activity returns the recent output state of the terminal.
func (r *Room) Activity() Activity {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Activity{LastOutput: r.act.last, BracketedPaste: r.act.paste, Prompt: approvalPrompt(r.act.tail), Setup: r.act.setup()}
}

var (
	// ansiRe matches escape sequences: CSI, OSC, and two-byte escapes.
	ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[=>78DEHMNOZc]`)
	// approvalRe matches the approval prompts of the five harnesses and
	// of common confirmations.
	// setupRe matches the first-run screens of the harnesses that wait for
	// the user before the program reads a prompt: the Antigravity welcome,
	// color scheme, and terms, the folder trust of Claude Code, Codex, and
	// Antigravity, and the sign-in and theme screens.
	setupRe = regexp.MustCompile(`(?i)(terms of service\s*(?:&|and)\s*data use|` +
		`welcome to antigravity cli|choose your color scheme|` +
		`do you trust the (?:files|contents) (?:in|of) this (?:folder|directory)|do you trust this folder|` +
		`yes, i trust this folder|is this a project you created or one you trust|allow codex to work in this folder|` +
		`how would you like to authenticate|select login method|sign in with chatgpt|choose the text style)`)
	// setupStepRe matches the key footer of an Antigravity onboarding step
	// whose title is not known; it counts only without a setupRe match.
	setupStepRe = regexp.MustCompile(`(?i)↑\s*/\s*↓\s*navigate\s*[·•]\s*enter\s+confirm`)
	approvalRe  = regexp.MustCompile(`(?i)(do you want to (?:proceed|make this edit|create|run|allow|apply)[^\n]*\?|would you like to (?:run|make|apply|allow)[^\n]*\?|allow (?:once|always|command|this (?:command|action|edit))|permission required|requires? (?:your )?approval|approve this[^\n]*|\(y/n\)|\[y/n\]|yes, and don't ask again)`)
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
func approvalPrompt(tail []byte) string { return lastMatchLine(approvalRe, tail) }

// lastMatchLine returns the line of the last match of re in the last
// screenful of output, or "".
// lastMatchTitle returns the last match of re in the last screenful of
// output, up to the end of its sentence (":", "?", "!") or the next gap
// of two spaces: a TUI draws other boxes (a preview, options) on the same
// screen line.
func lastMatchTitle(re *regexp.Regexp, tail []byte) string {
	text := screenText(tail)
	if r := []rune(text); len(r) > 2000 {
		text = string(r[len(r)-2000:])
	}
	loc := re.FindAllStringIndex(text, -1)
	if len(loc) == 0 {
		return ""
	}
	m := loc[len(loc)-1]
	rest := text[m[1]:]
	end := len(rest)
	if i := strings.IndexAny(rest, "\n:?!"); i >= 0 {
		end = i
		if rest[i] != '\n' {
			end++
		}
	}
	if i := strings.Index(rest[:end], "  "); i >= 0 {
		end = i
	}
	return strings.Join(strings.Fields(text[m[0]:m[1]]+rest[:end]), " ")
}

func lastMatchLine(re *regexp.Regexp, tail []byte) string {
	text := screenText(tail)
	if r := []rune(text); len(r) > 2000 {
		text = string(r[len(r)-2000:])
	}
	loc := re.FindAllStringIndex(text, -1)
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
