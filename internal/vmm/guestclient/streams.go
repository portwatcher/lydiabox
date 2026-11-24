package guestclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// PTY is an attachment to a guest PTY. Several attachments to one PTY form a
// terminal room: all see the same output and any may write.
type PTY interface {
	// ID returns the PTY ID.
	ID() string
	// Replay returns the ring-buffer snapshot (up to 1 MiB) sent on attach.
	Replay() []byte
	// Read returns live output after the replay. It returns io.EOF after the
	// process exited; Wait then returns the exit code.
	Read(p []byte) (int, error)
	// Write sends input.
	Write(p []byte) (int, error)
	// Resize changes the terminal size for every client in the room.
	Resize(cols, rows uint16) error
	// Signal sends SIGINT, SIGTERM, SIGHUP, or SIGQUIT to the foreground
	// process group.
	Signal(sig string) error
	// Wait blocks until the process exits and returns its exit code.
	Wait(ctx context.Context) (int, error)
	// Close detaches; the PTY keeps running in the guest.
	Close() error
}

// AttachPTY attaches to a running or exited PTY. It wakes the VM.
func (c *Client) AttachPTY(ctx context.Context, tenantID, id string) (PTY, error) {
	return c.attachPTY(ctx, tenantID, id, true)
}

// AttachRunningPTY is AttachPTY without a wake: it fails when the VM does
// not run.
func (c *Client) AttachRunningPTY(ctx context.Context, tenantID, id string) (PTY, error) {
	return c.attachPTY(ctx, tenantID, id, false)
}

func (c *Client) attachPTY(ctx context.Context, tenantID, id string, wake bool) (PTY, error) {
	var (
		st  *rpc.Stream
		res rpc.PTYAttachResult
	)
	err := c.do(ctx, tenantID, wake, func(s *rpc.Session) error {
		stream, raw, err := s.Open(ctx, rpc.MethodPTYAttach, rpc.PTYIDParams{ID: id})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			stream.Close()
			return fmt.Errorf("guestclient: decode attach result: %w", err)
		}
		st = stream
		return nil
	})
	if err != nil {
		return nil, err
	}
	typ, replay, err := st.ReadFrame()
	if err != nil || typ != rpc.FrameReplay {
		st.Close()
		return nil, fmt.Errorf("guestclient: read PTY replay: %w", errors.Join(err, errors.New("unexpected frame")))
	}
	c.track(tenantID, res.Info)
	return &ptyConn{c: c, tenantID: tenantID, info: res.Info, st: st, replay: replay}, nil
}

type ptyConn struct {
	c        *Client
	tenantID string
	info     rpc.PTYInfo
	st       *rpc.Stream
	replay   []byte

	// rmu serializes Read, which holds it while it blocks on the stream.
	rmu     sync.Mutex
	pending []byte
	// exit is set by Read and read by Wait without rmu, so Wait does not
	// block behind a Read that waits for output.
	exit atomic.Pointer[rpc.PTYEvent]
}

func (p *ptyConn) ID() string     { return p.info.ID }
func (p *ptyConn) Replay() []byte { return p.replay }

func (p *ptyConn) Read(b []byte) (int, error) {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	for len(p.pending) == 0 {
		if p.exit.Load() != nil {
			return 0, io.EOF
		}
		typ, payload, err := p.st.ReadFrame()
		if err != nil {
			return 0, err
		}
		switch typ {
		case rpc.FrameData:
			p.pending = payload
		case rpc.FrameJSON:
			var ev rpc.PTYEvent
			if json.Unmarshal(payload, &ev) != nil {
				continue
			}
			switch ev.Type {
			case "exit":
				p.exit.Store(&ev)
				return 0, io.EOF
			case "error":
				return 0, fmt.Errorf("guestclient: PTY %s: %s", p.info.ID, ev.Message)
			}
		}
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

func (p *ptyConn) Write(b []byte) (int, error) {
	if err := p.st.WriteData(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *ptyConn) Resize(cols, rows uint16) error {
	return p.st.WriteJSON(rpc.PTYControl{Type: "resize", Cols: cols, Rows: rows})
}

func (p *ptyConn) Signal(sig string) error {
	return p.st.WriteJSON(rpc.PTYControl{Type: "signal", Signal: sig})
}

func (p *ptyConn) Wait(ctx context.Context) (int, error) {
	if exit := p.exit.Load(); exit != nil {
		return exit.Code, nil
	}
	res, err := p.c.WaitPTY(ctx, p.tenantID, p.info.ID)
	if err != nil {
		return 0, err
	}
	return res.Code, nil
}

func (p *ptyConn) Close() error { return p.st.Close() }

// SignalStream is one WebRTC viewer's signaling channel to the guest.
type SignalStream struct {
	st    *rpc.Stream
	Hello rpc.SignalHello
}

// OpenSignal adds a desktop viewer with the ICE settings in p (the public
// IP of the forwarded WebRTC UDP port, the ICE servers, and the policy).
func (c *Client) OpenSignal(ctx context.Context, tenantID string, p rpc.SignalParams) (*SignalStream, error) {
	var out *SignalStream
	err := c.do(ctx, tenantID, true, func(s *rpc.Session) error {
		st, raw, err := s.Open(ctx, rpc.MethodDesktopSignal, p)
		if err != nil {
			return err
		}
		ss := &SignalStream{st: st}
		if err := json.Unmarshal(raw, &ss.Hello); err != nil {
			st.Close()
			return fmt.Errorf("guestclient: decode signal hello: %w", err)
		}
		out = ss
		return nil
	})
	return out, err
}

// Send forwards a browser message (offer or ice).
func (s *SignalStream) Send(msg json.RawMessage) error { return s.st.WriteFrame(rpc.FrameJSON, msg) }

// Recv returns the next guest message (answer, ice, or error).
func (s *SignalStream) Recv() (json.RawMessage, error) {
	for {
		typ, b, err := s.st.ReadFrame()
		if err != nil {
			return nil, err
		}
		if typ == rpc.FrameJSON {
			return b, nil
		}
	}
}

// Close removes the viewer.
func (s *SignalStream) Close() error { return s.st.Close() }

// EventStream delivers desktop events (agent_action and mode).
type EventStream struct{ st *rpc.Stream }

// SubscribeEvents opens the guest event stream without waking the VM.
func (c *Client) SubscribeEvents(ctx context.Context, tenantID string) (*EventStream, error) {
	var out *EventStream
	err := c.do(ctx, tenantID, false, func(s *rpc.Session) error {
		st, _, err := s.Open(ctx, rpc.MethodEventsSubscribe, nil)
		if err != nil {
			return err
		}
		out = &EventStream{st: st}
		return nil
	})
	return out, err
}

// Recv returns the next event as a JSON object.
func (e *EventStream) Recv() (json.RawMessage, error) {
	for {
		typ, b, err := e.st.ReadFrame()
		if err != nil {
			return nil, err
		}
		if typ == rpc.FrameJSON {
			return b, nil
		}
	}
}

// Close ends the subscription.
func (e *EventStream) Close() error { return e.st.Close() }
