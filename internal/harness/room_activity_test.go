package harness

import (
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
