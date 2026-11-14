//go:build unix

// Package daemon registers the guest daemon RPC methods (internal/guest/rpc
// method constants) on an rpc.Server, backed by the PTY manager, the X11
// controller, the MCP server, and the WebRTC streamer.
package daemon

import (
	"context"
	"encoding/json"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lydiabox/lydiabox/internal/guest/mcp"
	"github.com/lydiabox/lydiabox/internal/guest/ptymanager"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/webrtc"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
	"github.com/lydiabox/lydiabox/internal/version"
)

// Options are the services behind the RPC methods. Desktop, MCP, Apps, and
// Stream may be nil when the daemon runs without a display; the desktop
// methods then answer "unavailable".
type Options struct {
	PTY     *ptymanager.Manager
	Desktop *x11.Controller
	MCP     *mcp.Server
	Apps    mcp.Apps
	Stream  *webrtc.Streamer
}

type service struct{ Options }

func decode(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return rpc.Errorf(rpc.CodeBadRequest, "invalid params: %v", err)
	}
	return nil
}

// Register adds every guest method to srv.
func Register(srv *rpc.Server, opts Options) {
	s := &service{opts}
	srv.HandleUnary(rpc.MethodPing, func(context.Context, json.RawMessage) (any, error) {
		return rpc.PingResult{Version: version.Get().Version, Time: time.Now().UTC()}, nil
	})
	srv.HandleUnary(rpc.MethodSync, func(context.Context, json.RawMessage) (any, error) {
		unix.Sync()
		return nil, nil
	})
	srv.HandleUnary(rpc.MethodPTYStart, s.ptyStart)
	srv.HandleUnary(rpc.MethodPTYList, func(context.Context, json.RawMessage) (any, error) {
		return rpc.PTYListResult{PTYs: s.PTY.List()}, nil
	})
	srv.HandleUnary(rpc.MethodPTYWait, s.ptyWait)
	srv.HandleUnary(rpc.MethodPTYResize, s.ptyResize)
	srv.HandleUnary(rpc.MethodPTYSignal, s.ptySignal)
	srv.HandleUnary(rpc.MethodPTYKill, s.ptyKill)
	srv.Handle(rpc.MethodPTYAttach, s.ptyAttach)

	srv.HandleUnary(rpc.MethodToolsList, s.toolsList)
	srv.HandleUnary(rpc.MethodToolCall, s.toolCall)
	srv.HandleUnary(rpc.MethodModeGet, s.modeGet)
	srv.HandleUnary(rpc.MethodModeSet, s.modeSet)
	srv.HandleUnary(rpc.MethodDesktopLaunch, s.launch)
	srv.Handle(rpc.MethodDesktopSignal, s.signal)
	srv.Handle(rpc.MethodEventsSubscribe, s.events)
}

func (s *service) ptyStart(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.PTYStartParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	sess, err := s.PTY.Start(ptymanager.Spec{ID: p.ID, Command: p.Command, Dir: p.Dir, Env: p.Env, Cols: p.Cols, Rows: p.Rows})
	if err != nil {
		return nil, err
	}
	return sess.Info(), nil
}

func (s *service) session(params json.RawMessage) (*ptymanager.Session, error) {
	var p rpc.PTYIDParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	return s.PTY.Get(p.ID)
}

func (s *service) ptyWait(ctx context.Context, params json.RawMessage) (any, error) {
	sess, err := s.session(params)
	if err != nil {
		return nil, err
	}
	select {
	case <-sess.Done():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	code, sig := sess.Exit()
	return rpc.PTYExit{Code: code, Signal: sig}, nil
}

func (s *service) ptyResize(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.PTYResizeParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	sess, err := s.PTY.Get(p.ID)
	if err != nil {
		return nil, err
	}
	return nil, sess.Resize(p.Cols, p.Rows)
}

func (s *service) ptySignal(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.PTYSignalParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	sess, err := s.PTY.Get(p.ID)
	if err != nil {
		return nil, err
	}
	return nil, sess.Signal(p.Signal)
}

func (s *service) ptyKill(ctx context.Context, params json.RawMessage) (any, error) {
	var p rpc.PTYIDParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	return nil, s.PTY.Kill(ctx, p.ID)
}

// ptyAttach streams the replay and live output, and applies input and
// control frames from the client, until either side ends the stream.
func (s *service) ptyAttach(ctx context.Context, st *rpc.Stream, params json.RawMessage) error {
	sess, err := s.session(params)
	if err != nil {
		return st.ReplyError(err)
	}
	sub, replay := sess.Attach()
	defer sess.Detach(sub)
	if err := st.Reply(rpc.PTYAttachResult{Info: sess.Info(), ReplayBytes: len(replay)}); err != nil {
		return err
	}
	if err := st.WriteFrame(rpc.FrameReplay, replay); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		for {
			typ, b, err := st.ReadFrame()
			if err != nil {
				return
			}
			switch typ {
			case rpc.FrameData:
				_, _ = sess.Write(b)
			case rpc.FrameJSON:
				var c rpc.PTYControl
				if json.Unmarshal(b, &c) != nil {
					continue
				}
				switch c.Type {
				case "resize":
					if sess.Resize(c.Cols, c.Rows) == nil {
						_ = st.WriteJSON(rpc.PTYEvent{Type: "resized", Cols: c.Cols, Rows: c.Rows})
					}
				case "signal":
					_ = sess.Signal(c.Signal)
				}
			}
		}
	}()
	for {
		select {
		case chunk, ok := <-sub.C():
			if !ok {
				return s.attachEnded(ctx, st, sess, sub)
			}
			if err := st.WriteData(chunk); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *service) attachEnded(ctx context.Context, st *rpc.Stream, sess *ptymanager.Session, sub *ptymanager.Subscriber) error {
	if sess.Dropped(sub) {
		return st.WriteJSON(rpc.PTYEvent{Type: "error", Message: "client too slow; reattach to resume"})
	}
	select {
	case <-sess.Done():
	case <-ctx.Done():
		return nil
	}
	code, sig := sess.Exit()
	return st.WriteJSON(rpc.PTYEvent{Type: "exit", Code: code, Signal: sig})
}

func (s *service) needDesktop() error {
	if s.Desktop == nil || s.MCP == nil {
		return rpc.Errorf(rpc.CodeUnavailable, "the desktop is not enabled in this guest")
	}
	return nil
}

func (s *service) toolsList(context.Context, json.RawMessage) (any, error) {
	if err := s.needDesktop(); err != nil {
		return nil, err
	}
	return map[string]any{"tools": s.MCP.Tools()}, nil
}

func (s *service) toolCall(ctx context.Context, params json.RawMessage) (any, error) {
	if err := s.needDesktop(); err != nil {
		return nil, err
	}
	var p rpc.ToolCallParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	res, err := s.MCP.CallTool(ctx, p.Name, p.Arguments)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeNotFound, "%v", err)
	}
	return res, nil
}

func (s *service) modeGet(context.Context, json.RawMessage) (any, error) {
	if err := s.needDesktop(); err != nil {
		return nil, err
	}
	return s.Desktop.Mode(), nil
}

func (s *service) modeSet(_ context.Context, params json.RawMessage) (any, error) {
	if err := s.needDesktop(); err != nil {
		return nil, err
	}
	var p rpc.ModeSetParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	return s.Desktop.SetMode(p.Mode, p.By)
}

func (s *service) launch(ctx context.Context, params json.RawMessage) (any, error) {
	if err := s.needDesktop(); err != nil {
		return nil, err
	}
	var p rpc.LaunchParams
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	if err := s.Apps.Launch(ctx, p.App); err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	return nil, nil
}

// signal serves one WebRTC viewer for the lifetime of the stream.
func (s *service) signal(ctx context.Context, st *rpc.Stream, params json.RawMessage) error {
	if err := s.needDesktop(); err != nil {
		return st.ReplyError(err)
	}
	if s.Stream == nil {
		return st.ReplyError(rpc.Errorf(rpc.CodeUnavailable, "desktop streaming is not enabled"))
	}
	var p rpc.SignalParams
	if err := decode(params, &p); err != nil {
		return st.ReplyError(err)
	}
	w, h := s.Desktop.ScreenSize()
	if err := st.Reply(rpc.SignalHello{Mode: s.Desktop.Mode().Mode, Screen: rpc.Screen{Width: w, Height: h}}); err != nil {
		return err
	}
	peer, err := s.Stream.NewPeer(peerConfig(p), func(m webrtc.Signal) { _ = st.WriteJSON(m) })
	if err != nil {
		return st.WriteJSON(webrtc.Signal{Type: "error", Code: rpc.CodeUnavailable, Message: err.Error()})
	}
	defer peer.Close()
	for {
		typ, b, err := st.ReadFrame()
		if err != nil {
			return nil
		}
		if typ != rpc.FrameJSON {
			continue
		}
		if err := peer.HandleSignal(b); err != nil {
			_ = st.WriteJSON(webrtc.Signal{Type: "error", Code: rpc.CodeBadRequest, Message: err.Error()})
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// events streams desktop events until the client closes the stream.
func (s *service) events(ctx context.Context, st *rpc.Stream, _ json.RawMessage) error {
	if err := s.needDesktop(); err != nil {
		return st.ReplyError(err)
	}
	ch, unsubscribe := s.Desktop.Events().Subscribe()
	defer unsubscribe()
	if err := st.Reply(nil); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer cancel()
		for {
			if _, _, err := st.ReadFrame(); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case ev := <-ch:
			if err := st.WriteFrame(rpc.FrameJSON, ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}
