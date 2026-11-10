package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// Handler serves one stream. It must answer with s.Reply or s.ReplyError
// before it streams further frames. ctx is canceled when the server stops.
type Handler func(ctx context.Context, s *Stream, params json.RawMessage) error

// UnaryFunc serves a request/response method. ctx is also canceled when the
// client closes its stream.
type UnaryFunc func(ctx context.Context, params json.RawMessage) (any, error)

// Server dispatches streams of every accepted connection to handlers.
type Server struct {
	log *slog.Logger

	mu       sync.RWMutex
	handlers map[string]Handler

	connMu sync.Mutex
	conns  map[*yamux.Session]struct{}
	wg     sync.WaitGroup
}

// NewServer returns a server with no methods.
func NewServer(log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{log: log, handlers: map[string]Handler{}, conns: map[*yamux.Session]struct{}{}}
}

// Handle registers a streaming handler.
func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.handlers[method]; dup {
		panic("rpc: duplicate method " + method)
	}
	s.handlers[method] = h
}

// HandleUnary registers a request/response method.
func (s *Server) HandleUnary(method string, fn UnaryFunc) {
	s.Handle(method, func(ctx context.Context, st *Stream, params json.RawMessage) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// A unary client sends nothing after the request; a read returns
		// only when it closes or abandons the stream.
		go func() {
			var b [1]byte
			_, _ = st.conn.Read(b[:])
			cancel()
		}()
		res, err := fn(ctx, params)
		if err != nil {
			return st.ReplyError(err)
		}
		return st.Reply(res)
	})
}

// YamuxConfig is the session configuration used on both sides.
func YamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.Logger = nil
	cfg.KeepAliveInterval = 15 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.MaxStreamWindowSize = 1 << 20
	return cfg
}

// Serve accepts connections until ctx is canceled or ln fails. It closes ln
// and every session on return.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	defer s.closeAll()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.ServeConn(ctx, c)
		}()
	}
}

// ServeConn serves one already-accepted connection.
func (s *Server) ServeConn(ctx context.Context, c net.Conn) {
	sess, err := yamux.Server(c, YamuxConfig())
	if err != nil {
		c.Close()
		return
	}
	s.connMu.Lock()
	s.conns[sess] = struct{}{}
	s.connMu.Unlock()
	defer func() {
		s.connMu.Lock()
		delete(s.conns, sess)
		s.connMu.Unlock()
		sess.Close()
	}()
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		go s.serveStream(ctx, NewStream(st))
	}
}

func (s *Server) closeAll() {
	s.connMu.Lock()
	for sess := range s.conns {
		sess.Close()
	}
	s.connMu.Unlock()
	s.wg.Wait()
}

func (s *Server) serveStream(ctx context.Context, st *Stream) {
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(30 * time.Second))
	var req Request
	if err := st.ReadJSON(&req); err != nil {
		return
	}
	_ = st.SetDeadline(time.Time{})
	s.mu.RLock()
	h, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		_ = st.ReplyError(Errorf(CodeNoMethod, "unknown method %q", req.Method))
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()
	if err := h(ctx, st, req.Params); err != nil && !isClosedErr(err) {
		s.log.Debug("rpc handler failed", "method", req.Method, "err", err)
	}
}

func isClosedErr(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, yamux.ErrStreamClosed) ||
		errors.Is(err, yamux.ErrSessionShutdown) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, yamux.ErrConnectionReset)
}

// IsClosed reports whether err means the peer or the stream went away.
func IsClosed(err error) bool { return err != nil && isClosedErr(err) }
