// Package rpc is the multiplexed transport between the host control plane and
// the guest daemon (lydiabox-guest serve).
//
// One connection (AF_VSOCK on the chv driver, a UNIX socket on the docker
// driver and in tests) carries a yamux session. The host is the yamux client
// and opens one stream per call. Every stream starts with a Request frame from
// the host and a Response frame from the guest. Unary methods end there;
// streaming methods (pty.attach, desktop.signal, events.subscribe) keep
// exchanging frames until either side closes the stream.
//
// A frame is a 1-byte type, a 4-byte big-endian payload length, and the
// payload. FrameJSON carries one JSON value, FrameData raw bytes (PTY input
// and output), and FrameReplay the PTY ring-buffer snapshot sent on attach.
package rpc

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Frame types.
const (
	FrameJSON   byte = 'J'
	FrameData   byte = 'D'
	FrameReplay byte = 'R'
)

// MaxFrame bounds one frame payload (4 MiB). The PTY replay buffer (1 MiB)
// fits in one frame.
const MaxFrame = 4 << 20

// ErrFrameTooLarge is returned for a frame over MaxFrame.
var ErrFrameTooLarge = errors.New("rpc: frame too large")

// Stream is one framed, full-duplex stream. Writes are safe for concurrent
// use; reads must come from one goroutine.
type Stream struct {
	conn net.Conn
	r    *bufio.Reader
	wmu  sync.Mutex
	hdr  [5]byte
}

// NewStream wraps a connection or a yamux stream.
func NewStream(c net.Conn) *Stream {
	return &Stream{conn: c, r: bufio.NewReaderSize(c, 32<<10)}
}

// Conn returns the underlying connection.
func (s *Stream) Conn() net.Conn { return s.conn }

// WriteFrame writes one frame.
func (s *Stream) WriteFrame(typ byte, payload []byte) error {
	if len(payload) > MaxFrame {
		return ErrFrameTooLarge
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.hdr[0] = typ
	binary.BigEndian.PutUint32(s.hdr[1:], uint32(len(payload)))
	if _, err := s.conn.Write(s.hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := s.conn.Write(payload)
	return err
}

// WriteJSON writes v as a FrameJSON frame.
func (s *Stream) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.WriteFrame(FrameJSON, b)
}

// WriteData writes raw bytes as FrameData frames, splitting large buffers.
func (s *Stream) WriteData(p []byte) error {
	for len(p) > 0 {
		n := min(len(p), MaxFrame)
		if err := s.WriteFrame(FrameData, p[:n]); err != nil {
			return err
		}
		p = p[n:]
	}
	return nil
}

// ReadFrame reads the next frame.
func (s *Stream) ReadFrame() (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(s.r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFrame {
		return 0, nil, ErrFrameTooLarge
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(s.r, buf); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return hdr[0], buf, nil
}

// ReadJSON reads the next frame, which must be FrameJSON, into v.
func (s *Stream) ReadJSON(v any) error {
	typ, b, err := s.ReadFrame()
	if err != nil {
		return err
	}
	if typ != FrameJSON {
		return fmt.Errorf("rpc: expected JSON frame, got %q", typ)
	}
	return json.Unmarshal(b, v)
}

// SetDeadline sets the read and write deadline of the stream.
func (s *Stream) SetDeadline(t time.Time) error { return s.conn.SetDeadline(t) }

// Close closes the stream.
func (s *Stream) Close() error { return s.conn.Close() }

// Request is the first frame of every stream.
type Request struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers a Request.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes carried in Error.Code.
const (
	CodeBadRequest  = "bad_request"
	CodeNotFound    = "not_found"
	CodeConflict    = "conflict"
	CodeTakeover    = "takeover"
	CodeUnavailable = "unavailable"
	CodeInternal    = "internal"
	CodeNoMethod    = "no_method"
)

// Error is a structured RPC error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds an *Error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsCode reports whether err is an *Error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

func toError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeInternal, Message: err.Error()}
}

// Reply writes a successful Response with result (nil for {}).
func (s *Stream) Reply(result any) error {
	if result == nil {
		result = struct{}{}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.WriteJSON(Response{Result: b})
}

// ReplyError writes an error Response.
func (s *Stream) ReplyError(err error) error {
	return s.WriteJSON(Response{Error: toError(err)})
}
