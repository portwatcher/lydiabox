package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// PTYSubprotocol is the WebSocket subprotocol of terminal rooms.
const PTYSubprotocol = "lydiabox.pty.v1"

// ptyClientMsg is a control message from a terminal client.
type ptyClientMsg struct {
	Type   string `json:"type"`
	Cols   uint16 `json:"cols"`
	Rows   uint16 `json:"rows"`
	Signal string `json:"signal"`
	ID     int64  `json:"id"`
}

type ptyPong struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

var allowedSignals = map[string]bool{"SIGINT": true, "SIGTERM": true, "SIGHUP": true, "SIGQUIT": true}

const (
	wsWriteTimeout = 10 * time.Second
	// replayChunk splits the replay into frames.
	replayChunk = 64 << 10
)

// ws serves GET /api/v1/sessions/{id}/ws: a terminal room shared by every
// client of the session.
func (s SessionsRoutes) ws(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{PTYSubprotocol}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != PTYSubprotocol {
		conn.Close(websocket.StatusPolicyViolation, "subprotocol "+PTYSubprotocol+" required")
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var room *harness.Room
	if r.URL.Query().Get("reconnect") == "1" {
		// An automatic reconnect of the client: not a demand for a paused VM.
		room, err = s.Manager.Reconnect(ctx, tenant.FromContext(ctx), r.PathValue("id"))
	} else {
		room, err = s.Manager.Room(ctx, tenant.FromContext(ctx), r.PathValue("id"))
	}
	if errors.Is(err, harness.ErrVMPaused) {
		writeControl(ctx, conn, harness.CtlError{Type: "error", Code: CodeVMPaused, Message: "the VM is hibernating"})
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	if errors.Is(err, ErrVMStopped) {
		// The user stopped the VM: the client does not retry.
		writeControl(ctx, conn, harness.CtlError{Type: "error", Code: CodeVMStopped, Message: "the VM was stopped"})
		conn.Close(websocket.StatusNormalClosure, "")
		return
	}
	if err != nil {
		_, code := harnessStatus(err)
		writeControl(ctx, conn, harness.CtlError{Type: "error", Code: code, Message: err.Error()})
		conn.Close(closeStatusFor(err), "")
		return
	}
	sub, hello, replay, ok := room.Subscribe()
	if !ok {
		writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "conflict", Message: "the session terminal has exited"})
		conn.Close(websocket.StatusPolicyViolation, "")
		return
	}
	defer room.Unsubscribe(sub)
	if err := writeControl(ctx, conn, hello); err != nil {
		return
	}
	for off := 0; off < len(replay); off += replayChunk {
		end := min(off+replayChunk, len(replay))
		if err := writeBinary(ctx, conn, replay[off:end]); err != nil {
			return
		}
	}

	go func() {
		defer cancel()
		readLoop(ctx, conn, room)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-sub.C:
			if !ok {
				// Dropped as a slow client, or the room ended without an
				// exit frame (switch failure already sent an error).
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
			var err error
			switch {
			case f.Control != nil:
				err = writeControl(ctx, conn, f.Control)
			case f.Data != nil:
				err = writeBinary(ctx, conn, f.Data)
			}
			if err != nil {
				return
			}
			if f.Close {
				conn.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}
}

func closeStatusFor(err error) websocket.StatusCode {
	switch {
	case errors.Is(err, harness.ErrUnavailable):
		return websocket.StatusTryAgainLater
	case errors.Is(err, harness.ErrNotFound), errors.Is(err, harness.ErrConflict), errors.Is(err, harness.ErrBadRequest):
		return websocket.StatusPolicyViolation
	}
	return websocket.StatusInternalError
}

// readLoop forwards client input to the room until the socket closes.
func readLoop(ctx context.Context, conn *websocket.Conn, room *harness.Room) {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if err := room.Write(data); err != nil {
				writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "unavailable", Message: "terminal input failed: " + err.Error()})
			}
			continue
		}
		var msg ptyClientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "bad_request", Message: "invalid control message"})
			continue
		}
		switch msg.Type {
		case "resize":
			if msg.Cols == 0 || msg.Rows == 0 || msg.Cols > 1000 || msg.Rows > 1000 {
				writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "bad_request", Message: "cols and rows must be 1 to 1000"})
				continue
			}
			if err := room.Resize(msg.Cols, msg.Rows); err != nil {
				writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "unavailable", Message: err.Error()})
			}
		case "signal":
			if !allowedSignals[msg.Signal] {
				writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "bad_request", Message: "signal must be SIGINT, SIGTERM, SIGHUP, or SIGQUIT"})
				continue
			}
			if err := room.Signal(msg.Signal); err != nil {
				writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "unavailable", Message: err.Error()})
			}
		case "ping":
			writeControl(ctx, conn, ptyPong{Type: "pong", ID: msg.ID})
		default:
			writeControl(ctx, conn, harness.CtlError{Type: "error", Code: "bad_request", Message: "unknown message type " + msg.Type})
		}
	}
}

// writeControl and writeBinary may be called from the read and write
// goroutines; coder/websocket serializes concurrent writers.
func writeControl(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

func writeBinary(ctx context.Context, conn *websocket.Conn, b []byte) error {
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageBinary, b)
}
