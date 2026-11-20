package harness

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// Events implements EventSource. The guest reads the session files on the
// machine disk. A call with wake starts or resumes the machine (the user
// opened the session); a call without wake does not, and fails while the
// machine does not run. A guest program without harness.events gives the
// events of the plain transcript.
func (g *GuestDiscovery) Events(ctx context.Context, tenantID, harness, nativeID string, after int, wake bool) (EventPage, error) {
	switch {
	case wake && g.Begin != nil:
		release, err := g.Begin(ctx, tenantID)
		if err != nil {
			return EventPage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		defer release()
	case !wake && g.Running != nil && !g.Running(tenantID):
		return EventPage{}, fmt.Errorf("%w: the machine does not run", ErrUnavailable)
	}
	var page EventPage
	err := g.Guest.FSCall(ctx, tenantID, rpc.MethodHarnessEvents, rpc.HarnessEventsParams{Harness: harness, ID: nativeID, After: after}, &page)
	if rpc.IsCode(err, rpc.CodeNoMethod) {
		var t Transcript
		if err := g.Guest.FSCall(ctx, tenantID, rpc.MethodHarnessTranscript, rpc.HarnessSessionParams{Harness: harness, ID: nativeID}, &t); err != nil {
			return EventPage{}, guestErr(err, "read the transcript in the guest")
		}
		return EventPageOf(EventsFromTranscript(t), after), nil
	}
	if err != nil {
		return EventPage{}, guestErr(err, "read the session events in the guest")
	}
	return page, nil
}

// WakeForEvents implements EventWaker with the wake of an event read: it
// takes a files lease, starts or resumes the machine, and releases the
// lease. A machine that the user stopped is not started.
func (g *GuestDiscovery) WakeForEvents(ctx context.Context, tenantID string) error {
	if g.Begin == nil {
		return nil
	}
	release, err := g.Begin(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	release()
	return nil
}

// Serving implements servingReporter: the machine runs and no pause or stop
// of it is in progress.
func (g *GuestDiscovery) Serving(tenantID string) bool {
	return g.Running == nil || g.Running(tenantID)
}

func guestErr(err error, what string) error {
	switch {
	case rpc.IsCode(err, rpc.CodeBadRequest):
		return fmt.Errorf("%w: %v", ErrBadRequest, err)
	case rpc.IsCode(err, rpc.CodeNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return err
	}
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, what, err)
}

// EventsFromTranscript converts a plain transcript to events (without tool
// results, reasoning, or usage).
func EventsFromTranscript(t Transcript) []Event {
	out := make([]Event, 0, len(t.Entries))
	for i, e := range t.Entries {
		key := "t" + strconv.Itoa(i)
		switch e.Role {
		case RoleUser:
			out = append(out, Event{Key: key, Kind: EventUser, Time: e.Time, Text: e.Text})
		case RoleAssistant:
			out = append(out, Event{Key: key, Kind: EventAssistant, Time: e.Time, Text: e.Text})
		case RoleTool:
			kind := ToolKind(e.Tool)
			if kind == ToolShell {
				kind = ShellKind(e.Text)
			}
			out = append(out, Event{Key: "tool:" + key, Kind: EventTool, Time: e.Time,
				Tool: &ToolCall{ID: key, Name: e.Tool, Kind: kind, Summary: e.Text}})
		}
	}
	return out
}
