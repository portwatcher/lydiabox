package harness

import (
	"context"
	"sync"
	"time"
)

// ReplayLimit bounds the terminal replay a new client receives (1 MiB).
const ReplayLimit = 1 << 20

// Control messages sent to terminal clients (docs/api.md section 8).
type (
	CtlHello struct {
		Type        string `json:"type"`
		SessionID   string `json:"sessionId"`
		ReplayBytes int    `json:"replayBytes"`
		Cols        uint16 `json:"cols"`
		Rows        uint16 `json:"rows"`
	}
	CtlResized struct {
		Type string `json:"type"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	CtlExit struct {
		Type   string `json:"type"`
		Code   int    `json:"code"`
		Signal string `json:"signal"`
	}
	CtlError struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
)

// Frame is one message to a terminal client: raw output bytes or a control
// message.
type Frame struct {
	Data    []byte
	Control any
	// Close asks the client handler to close the socket after this frame.
	Close bool
}

// Subscriber receives the frames of a room. C is closed when the room ends
// or the subscriber fell too far behind.
type Subscriber struct {
	C    chan Frame
	once sync.Once
}

func (s *Subscriber) close() { s.once.Do(func() { close(s.C) }) }

// subscriberBuffer is how many frames a slow client may lag behind.
const subscriberBuffer = 512

// Room is a terminal shared by every client attached to one session. It
// pumps PTY output to all subscribers and keeps a replay ring.
type Room struct {
	mu        sync.Mutex
	sessionID string
	pty       PTY
	ring      []byte
	subs      map[*Subscriber]struct{}
	cols      uint16
	rows      uint16
	done      bool
	onExit    func(code int)
	writeMu   sync.Mutex
}

func newRoom(sessionID string, p PTY, cols, rows uint16, onExit func(int)) *Room {
	r := &Room{sessionID: sessionID, pty: p, subs: map[*Subscriber]struct{}{}, cols: cols, rows: rows, onExit: onExit}
	r.appendRing(p.Replay())
	go r.pump(p)
	return r
}

func (r *Room) appendRing(b []byte) {
	r.ring = append(r.ring, b...)
	if over := len(r.ring) - ReplayLimit; over > 0 {
		r.ring = append(r.ring[:0:0], r.ring[over:]...)
	}
}

func (r *Room) pump(p PTY) {
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			r.mu.Lock()
			r.appendRing(chunk)
			r.broadcastLocked(Frame{Data: chunk})
			r.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	code, werr := p.Wait(ctx)
	cancel()
	if werr != nil {
		code = -1
	}
	r.mu.Lock()
	r.done = true
	onExit := r.onExit
	r.broadcastLocked(Frame{Control: CtlExit{Type: "exit", Code: code}, Close: true})
	for s := range r.subs {
		s.close()
	}
	r.subs = map[*Subscriber]struct{}{}
	r.mu.Unlock()
	if onExit != nil {
		onExit(code)
	}
}

func (r *Room) broadcastLocked(f Frame) {
	for s := range r.subs {
		select {
		case s.C <- f:
		default:
			// The client cannot keep up; drop it rather than stall the PTY.
			delete(r.subs, s)
			s.close()
		}
	}
}

// Subscribe attaches a client. It returns the hello message and the replay;
// live frames follow on the subscriber channel. ok is false when the
// terminal already exited.
func (r *Room) Subscribe() (*Subscriber, CtlHello, []byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil, CtlHello{}, nil, false
	}
	s := &Subscriber{C: make(chan Frame, subscriberBuffer)}
	r.subs[s] = struct{}{}
	replay := append([]byte(nil), r.ring...)
	return s, CtlHello{Type: "hello", SessionID: r.sessionID, ReplayBytes: len(replay), Cols: r.cols, Rows: r.rows}, replay, true
}

// Unsubscribe detaches a client.
func (r *Room) Unsubscribe(s *Subscriber) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.subs[s]; ok {
		delete(r.subs, s)
		s.close()
	}
}

// Clients returns the number of attached clients.
func (r *Room) Clients() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}

// SessionID returns the API ID of the session the room shows now.
func (r *Room) SessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessionID
}

func (r *Room) current() PTY {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pty
}

// Write sends input to the PTY.
func (r *Room) Write(p []byte) error {
	pty := r.current()
	if pty == nil {
		return ErrUnavailable
	}
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	_, err := pty.Write(p)
	return err
}

// Resize resizes the PTY and tells every client.
func (r *Room) Resize(cols, rows uint16) error {
	pty := r.current()
	if pty == nil {
		return ErrUnavailable
	}
	if err := pty.Resize(cols, rows); err != nil {
		return err
	}
	r.mu.Lock()
	r.cols, r.rows = cols, rows
	r.broadcastLocked(Frame{Control: CtlResized{Type: "resized", Cols: cols, Rows: rows}})
	r.mu.Unlock()
	return nil
}

// Signal sends a signal to the PTY process.
func (r *Room) Signal(sig string) error {
	pty := r.current()
	if pty == nil {
		return ErrUnavailable
	}
	return pty.Signal(sig)
}
