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
		"> 1. Sign in with ChatGPT\r\n   Usage included with Plus, Pro, and Team plans":                         "> 1. Sign in with ChatGPT",
		"Do you trust the contents of this directory? Working with untrusted contents":                          "Do you trust the contents of this directory? Working with untrusted contents",
		"Quick safety check: Is this a project you created or one you trust?\r\n ❯ 1. Yes, I trust this folder": "❯ 1. Yes, I trust this folder",
		"I finished the change. The tests pass.\r\n> ":                                                          "",
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
