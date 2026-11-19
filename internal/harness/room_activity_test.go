package harness

import (
	"strings"
	"testing"
	"time"
)

func TestApprovalPrompt(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[2J\x1b[1;1H\x1b[1mBash command\x1b[0m\r\n  rm -rf build\r\n Do you want to proceed?\r\n ❯ 1. Yes": "Do you want to proceed?",
		"Would you like to run the following command?\r\n$ make test":                                           "Would you like to run the following command?",
		"\x1b[38;5;3m△ Permission required\x1b[0m\r\n  Allow once   Allow always   Reject":                      "Allow once Allow always Reject",
		"Overwrite the file? (y/n) ":                   "Overwrite the file? (y/n)",
		"I finished the change. The tests pass.\r\n> ": "",
	} {
		if got := approvalPrompt([]byte(in)); got != want {
			t.Errorf("approvalPrompt(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestActivityPasteMode(t *testing.T) {
	var a activity
	a.note([]byte("start\x1b[?20"), time.Now())
	a.note([]byte("04h ready"), time.Now())
	if !a.paste {
		t.Fatal("a split mode switch was not seen")
	}
	a.note([]byte("\x1b[?2004l"), time.Now())
	if a.paste {
		t.Fatal("paste mode stayed on")
	}
	big := make([]byte, activityTail*2)
	a.note(big, time.Now())
	if len(a.tail) != activityTail {
		t.Fatalf("tail = %d bytes", len(a.tail))
	}
}

func TestCleanInput(t *testing.T) {
	if got := cleanInput("a\r\nb\rc\x1b[201~\x03\td"); got != "a\nb\nc[201~\td" {
		t.Fatalf("cleanInput = %q", got)
	}
}

func TestSetupScreen(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[2J\x1b[1;1H\x1b[1mTerms of Service & Data Use\x1b[0m\r\n By continuing you agree":                 "Terms of Service & Data Use",
		" Choose the text style that looks best with your terminal:\r\n ❯ 1. Dark mode":                         "Choose the text style that looks best with your terminal:",
		"> 1. Sign in with ChatGPT\r\n   Usage included with Plus, Pro, and Team plans":                         "Sign in with ChatGPT",
		"Do you trust the contents of this directory? Working with untrusted contents":                          "Do you trust the contents of this directory?",
		"Quick safety check: Is this a project you created or one you trust?\r\n ❯ 1. Yes, I trust this folder": "Yes, I trust this folder",
		"I finished the change. The tests pass.\r\n> ":                                                          "",
		agyColorScheme: "Choose your color scheme:",
		// the test machine: cursor moves put the preview box on the title line.
		"Choose your color scheme:\x1b[40C\x1b[1m╭ Preview ╮\x1b[0m\x1b[2C you: add a greeting function": "Choose your color scheme:",
		"\x1b[1mWelcome to Antigravity CLI!\x1b[0m\r\n":                                                  "Welcome to Antigravity CLI!",
		"\x1b[2J\x1b[HPick a region\r\n> europe\r\n\x1b[2m↑/↓ Navigate · enter Confirm\x1b[0m":           "↑/↓ Navigate · enter Confirm",
	} {
		var a activity
		a.note([]byte(in), time.Now())
		if got := a.setup(); got != want {
			t.Errorf("setup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetupScreenEndsAfterInput(t *testing.T) {
	var a activity
	a.note([]byte("Terms of Service & Data Use\r\n> 1. Accept\r\n  2. Decline"), time.Now())
	a.input()
	if a.setup() == "" {
		t.Fatal("the setup screen ended before the program wrote again")
	}
	// An arrow key: the program draws the same screen again.
	a.note([]byte("\x1b[3A\x1b[2KTerms of Service & Data Use\r\n  1. Accept\r\n> 2. Decline"), time.Now())
	if a.setup() == "" {
		t.Fatal("a redraw of the setup screen ended it")
	}
	// Enter: the program shows its prompt. The old text stays in the tail.
	a.input()
	a.note([]byte("\x1b[2J\x1b[H╭────╮\r\n│ > Type your message │\r\n╰────╯"), time.Now())
	if got := a.setup(); got != "" {
		t.Fatalf("setup = %q after the user accepted", got)
	}
	if !strings.Contains(string(a.tail), "Terms of Service") {
		t.Fatal("test premise: the old screen is in the tail")
	}
}

// agyColorScheme is the first onboarding step of Antigravity CLI on a new
// home (seen on a production tenant).
const agyColorScheme = "\x1b[2J\x1b[H\x1b[1mWelcome to Antigravity CLI!\x1b[0m\r\n\r\n" +
	"Choose your color scheme:\r\n" +
	"\x1b[36m> terminal\x1b[0m\r\n  light\r\n  solarized light\r\n  colorblind-friendly light\r\n  dark\r\n" +
	"  solarized dark\r\n  colorblind-friendly dark\r\n  tokyo night\r\n" +
	"╭── Preview ──╮\r\n│ func main() {} │\r\n╰─────╯\r\n" +
	"\x1b[7m[Next]\x1b[0m\r\n\x1b[2m↑/↓ Navigate · enter Confirm\x1b[0m"

// The Antigravity onboarding is setup from the color scheme to the terms,
// and ends only when the program shows other output.
func TestSetupAntigravityOnboarding(t *testing.T) {
	var a activity
	a.note([]byte(agyColorScheme), time.Now())
	if got := a.setup(); got != "Choose your color scheme:" {
		t.Fatalf("color scheme step = %q", got)
	}
	a.input()
	a.note([]byte("\x1b[2J\x1b[H\x1b[1mTerms of Service & Data Use\x1b[0m\r\n[Accept] [Decline]\r\n↑/↓ Navigate · enter Confirm"), time.Now())
	if got := a.setup(); got != "Terms of Service & Data Use" {
		t.Fatalf("terms step = %q", got)
	}
	a.input()
	a.note([]byte("\x1b[2J\x1b[H> Ask anything\r\n"), time.Now())
	if got := a.setup(); got != "" {
		t.Fatalf("setup = %q at the prompt", got)
	}
}
