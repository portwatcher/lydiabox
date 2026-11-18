package harness

import (
	"strings"
	"testing"
)

// TestTranscriptFit keeps the newest entries and cuts long texts.
func TestTranscriptFit(t *testing.T) {
	long := strings.Repeat("é", MaxEntryText) // two bytes per rune
	var tr Transcript
	for range 200 {
		tr.Entries = append(tr.Entries, TranscriptEntry{Role: RoleUser, Text: long})
	}
	tr.Entries = append(tr.Entries, TranscriptEntry{Role: RoleAssistant, Text: "last"})
	got := tr.Fit()
	if !got.Truncated || len(got.Entries) == 0 || len(got.Entries) >= len(tr.Entries) {
		t.Fatalf("truncated=%v entries=%d", got.Truncated, len(got.Entries))
	}
	if last := got.Entries[len(got.Entries)-1]; last.Text != "last" {
		t.Fatalf("newest entry dropped: %q", last.Text)
	}
	size := 0
	for _, e := range got.Entries[:len(got.Entries)-1] {
		if len(e.Text) > MaxEntryText+len("…") || !strings.HasSuffix(e.Text, "…") || !strings.HasPrefix(e.Text, "é") {
			t.Fatalf("text not cut on a rune boundary: %d bytes", len(e.Text))
		}
		size += len(e.Text)
	}
	if size > MaxTranscriptBytes {
		t.Fatalf("%d bytes over the bound", size)
	}
	small := Transcript{Entries: []TranscriptEntry{{Role: RoleUser, Text: "hi"}}}.Fit()
	if small.Truncated || len(small.Entries) != 1 {
		t.Fatalf("small transcript changed: %+v", small)
	}
}
