package harness

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"
)

// Roles of a transcript entry.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	// RoleTool is a tool call of the agent: Tool names the tool and Text
	// is a short form of its input.
	RoleTool = "tool"
)

// TranscriptEntry is one message or tool call of a session transcript.
type TranscriptEntry struct {
	Role string    `json:"role"`
	Text string    `json:"text"`
	Tool string    `json:"tool,omitempty"`
	Time time.Time `json:"time,omitzero"`
}

// Transcript is the read-only history of one session.
type Transcript struct {
	Entries []TranscriptEntry `json:"entries"`
	// Truncated reports that older entries or long texts were cut to fit
	// MaxTranscriptBytes.
	Truncated bool `json:"truncated,omitempty"`
}

// Transcriber reads the transcript of one session. homeDir is the path of
// the guest /home/agent on the machine that runs the adapter.
type Transcriber interface {
	Transcript(homeDir, sessionID string) (Transcript, error)
}

// Bounds of a transcript: it crosses the guest RPC link in one frame
// (rpc.MaxFrame is 4 MiB) and is rendered in one page.
const (
	MaxTranscriptBytes = 3 << 20
	MaxEntryText       = 32 << 10
)

// Fit cuts long texts and drops the oldest entries until the texts fit
// MaxTranscriptBytes. The newest entries are the most useful to continue a
// session.
func (t Transcript) Fit() Transcript {
	total := 0
	start := len(t.Entries)
	out := Transcript{Truncated: t.Truncated}
	for i := len(t.Entries) - 1; i >= 0; i-- {
		e := t.Entries[i]
		if len(e.Text) > MaxEntryText {
			e.Text = cutUTF8(e.Text, MaxEntryText) + "…"
			out.Truncated = true
		}
		if total+len(e.Text)+64 > MaxTranscriptBytes {
			out.Truncated = true
			break
		}
		total += len(e.Text) + 64
		t.Entries[i] = e
		start = i
	}
	out.Entries = append([]TranscriptEntry{}, t.Entries[start:]...)
	return out
}

func cutUTF8(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Transcript returns the read-only history of a session. In the provider
// storage mode it starts or resumes the machine that holds the files.
func (m *Manager) Transcript(ctx context.Context, tenantID, id string) (Transcript, error) {
	h, nativeID, err := SplitSessionID(id)
	if err != nil {
		return Transcript{}, err
	}
	if h == Shell {
		return Transcript{}, fmt.Errorf("%w: a shell session has no transcript", ErrNotFound)
	}
	if _, ok := m.adapters[h]; !ok {
		return Transcript{}, fmt.Errorf("%w: unknown harness %q", ErrNotFound, h)
	}
	if ls := m.lookupLive(tenantID, id); ls != nil {
		h, nativeID = ls.harness, ls.nativeID
	}
	if nativeID == "" {
		return Transcript{}, fmt.Errorf("%w: session %s has no transcript yet", ErrNotFound, id)
	}
	t, err := m.opts.Discovery.Transcript(ctx, tenantID, h, nativeID)
	if err != nil {
		return Transcript{}, err
	}
	return t.Fit(), nil
}
