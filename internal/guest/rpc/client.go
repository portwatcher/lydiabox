package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

// Session is the client side of one multiplexed connection.
type Session struct {
	mux *yamux.Session
}

// NewSession starts a yamux client session on conn.
func NewSession(conn net.Conn) (*Session, error) {
	mux, err := yamux.Client(conn, YamuxConfig())
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("rpc: start session: %w", err)
	}
	return &Session{mux: mux}, nil
}

// IsClosed reports whether the session has shut down.
func (s *Session) IsClosed() bool { return s.mux.IsClosed() }

// CloseChan is closed when the session shuts down.
func (s *Session) CloseChan() <-chan struct{} { return s.mux.CloseChan() }

// Close closes the session and every stream.
func (s *Session) Close() error { return s.mux.Close() }

// Ping measures the round trip of the yamux keepalive.
func (s *Session) Ping() (time.Duration, error) { return s.mux.Ping() }

// Open opens a stream, sends the request, and waits for the response. On
// success the stream stays open for streaming methods; the caller closes it.
// ctx bounds only the request/response exchange.
func (s *Session) Open(ctx context.Context, method string, params any) (*Stream, json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.mux.OpenStream()
	if err != nil {
		return nil, nil, fmt.Errorf("rpc: open stream: %w", err)
	}
	st := NewStream(c)
	// Only the AfterFunc sets the deadline, so a timeout always comes after
	// ctx is done and the caller gets ctx.Err() instead of a socket timeout.
	stop := context.AfterFunc(ctx, func() { _ = st.SetDeadline(time.Unix(1, 0)) })
	res, err := st.exchange(method, raw)
	if !stop() || err != nil {
		st.Close()
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if err == nil {
			err = context.Canceled
		}
		return nil, nil, err
	}
	_ = st.SetDeadline(time.Time{})
	return st, res, nil
}

func (st *Stream) exchange(method string, params json.RawMessage) (json.RawMessage, error) {
	if err := st.WriteJSON(Request{Method: method, Params: params}); err != nil {
		return nil, fmt.Errorf("rpc: %s: write request: %w", method, err)
	}
	var resp Response
	if err := st.ReadJSON(&resp); err != nil {
		return nil, fmt.Errorf("rpc: %s: read response: %w", method, err)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// Call performs a unary call and decodes the result into out (may be nil).
// Canceling ctx abandons the call, which also cancels it on the guest.
func (s *Session) Call(ctx context.Context, method string, params, out any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	c, err := s.mux.OpenStream()
	if err != nil {
		return fmt.Errorf("rpc: open stream: %w", err)
	}
	st := NewStream(c)
	defer st.Close()
	// Only the AfterFunc sets the deadline, so a timeout always comes after
	// ctx is done and the caller gets ctx.Err() instead of a socket timeout.
	stop := context.AfterFunc(ctx, func() { _ = st.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	res, err := st.exchange(method, raw)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if out != nil && len(res) > 0 {
		if err := json.Unmarshal(res, out); err != nil {
			return fmt.Errorf("rpc: %s: decode result: %w", method, err)
		}
	}
	return nil
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("rpc: encode params: %w", err)
	}
	return b, nil
}
