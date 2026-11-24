package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"

	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/httpx"
	"github.com/portwatcher/lydiabox/internal/tenant"
)

// EventsSubprotocol is the WebSocket subprotocol of session event streams.
const EventsSubprotocol = "lydiabox.events.v1"

// eventsMsg carries events: reset replaces every event of the client.
type eventsMsg struct {
	Type   string          `json:"type"`
	Reset  bool            `json:"reset,omitempty"`
	Events []harness.Event `json:"events"`
}

type statusMsg struct {
	Type string `json:"type"`
	harness.EventStatus
}

// events serves GET /api/v1/sessions/{id}/events: the typed events of a
// session (first all events read so far, then the changes) and its state.
func (s SessionsRoutes) events(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{EventsSubprotocol}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != EventsSubprotocol {
		conn.Close(websocket.StatusPolicyViolation, "subprotocol "+EventsSubprotocol+" required")
		return
	}
	conn.SetReadLimit(64 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub, first, done, err := s.Manager.SubscribeEvents(tenant.FromContext(ctx), r.PathValue("id"))
	if err != nil {
		_, code := harnessStatus(err)
		writeControl(ctx, conn, harness.CtlError{Type: "error", Code: code, Message: err.Error()})
		conn.Close(closeStatusFor(err), "")
		return
	}
	defer done()
	go func() {
		defer cancel()
		pingLoop(ctx, conn)
	}()
	if err := writeUpdate(ctx, conn, first); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-sub.C:
			if !ok {
				// Dropped as a slow client: it reconnects and gets a reset.
				conn.Close(websocket.StatusTryAgainLater, "too slow")
				return
			}
			if err := writeUpdate(ctx, conn, u); err != nil {
				return
			}
		}
	}
}

// writeUpdate sends the events of u in parts that fit a frame, then its
// status.
func writeUpdate(ctx context.Context, conn *websocket.Conn, u harness.EventUpdate) error {
	evs, reset := u.Events, u.Reset
	for reset || len(evs) > 0 {
		page := harness.EventPageOf(evs, 0)
		if err := writeControl(ctx, conn, eventsMsg{Type: "events", Reset: reset, Events: page.Events}); err != nil {
			return err
		}
		evs, reset = evs[page.Next:], false
	}
	if u.Status != nil {
		return writeControl(ctx, conn, statusMsg{Type: "status", EventStatus: *u.Status})
	}
	return nil
}

// pingLoop answers the pings of a client until the socket closes.
func pingLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg ptyClientMsg
		if json.Unmarshal(data, &msg) == nil && msg.Type == "ping" {
			writeControl(ctx, conn, ptyPong{Type: "pong", ID: msg.ID})
		}
	}
}

// inputRequest is the body of POST /api/v1/sessions/{id}/input.
type inputRequest struct {
	Text string `json:"text"`
}

// input types a prompt into the terminal of a live session and submits it.
func (s SessionsRoutes) input(w http.ResponseWriter, r *http.Request) {
	var req inputRequest
	if err := httpx.DecodeJSON(w, r, harness.MaxInputBytes*2, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if err := s.Manager.SendInput(r.Context(), tenant.FromContext(r.Context()), r.PathValue("id"), req.Text); err != nil {
		writeHarnessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// permissionRequest answers a permission request of an ACP agent.
type permissionRequest struct {
	RequestID string `json:"requestId"`
	OptionID  string `json:"optionId"`
}

// permission answers a permission request of the chat agent of a session.
func (s SessionsRoutes) permission(w http.ResponseWriter, r *http.Request) {
	var req permissionRequest
	if err := httpx.DecodeJSON(w, r, 0, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	if err := s.Manager.AnswerPermission(r.Context(), tenant.FromContext(r.Context()), r.PathValue("id"), req.RequestID, req.OptionID); err != nil {
		writeHarnessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// interrupt stops the current turn of a live session.
func (s SessionsRoutes) interrupt(w http.ResponseWriter, r *http.Request) {
	if err := s.Manager.Interrupt(r.Context(), tenant.FromContext(r.Context()), r.PathValue("id")); err != nil {
		writeHarnessError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
