// Package guestclient is the host-side client of the guest daemon RPC
// (internal/guest/rpc). It keeps one multiplexed connection per tenant over
// the VMM driver's DialGuest (vsock port 9000 or the docker UNIX socket),
// wakes the VM on demand, and holds a "pty" lease on the VM controller for
// every running PTY so the VM is not hibernated under a working harness.
package guestclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// Dialer connects to a guest vsock port (vmm.Driver implements it).
type Dialer interface {
	DialGuest(ctx context.Context, tenantID string, port uint32) (net.Conn, error)
}

// Waker boots or resumes the VM (vmm.Controller implements it).
type Waker interface {
	EnsureRunning(ctx context.Context, tenantID string) error
}

// Leaser tracks activity that keeps the VM awake (vmm.Controller).
type Leaser interface {
	AcquireLease(tenantID, kind string) (release func())
}

// LeasePTY is the lease kind held for each running PTY.
const LeasePTY = "pty"

// PTYInfo describes a guest PTY.
type PTYInfo = rpc.PTYInfo

// PTYExit is a PTY exit status.
type PTYExit = rpc.PTYExit

// ModeState is the desktop control mode.
type ModeState = rpc.ModeState

// PTYSpec describes a PTY to start. An empty ID lets the guest choose one.
type PTYSpec struct {
	ID      string
	Command []string
	Dir     string
	Env     map[string]string
	Cols    uint16
	Rows    uint16
}

// Options configures a Client.
type Options struct {
	Dialer Dialer
	// Waker, when set, is called before every waking call.
	Waker Waker
	// WakeTimeout bounds the wait of one waking call for the Waker
	// (default DefaultWakeTimeout). Most waking calls serve an HTTP
	// request; a proxy such as Cloudflare ends a request after 100 s. A
	// longer start continues, and the call fails with the Waker's error
	// (vmm.ErrStarting).
	WakeTimeout time.Duration
	// Leaser, when set, receives a pty lease per running PTY.
	Leaser Leaser
	Logger *slog.Logger
}

// DefaultWakeTimeout is the default of Options.WakeTimeout.
const DefaultWakeTimeout = 75 * time.Second

// Client talks to the guest daemon of each tenant.
type Client struct {
	opts Options

	mu       sync.Mutex
	sessions map[string]*rpc.Session
	dialMu   map[string]*sync.Mutex
	leases   map[string]map[string]bool
}

// New returns a client.
func New(opts Options) *Client {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.WakeTimeout <= 0 {
		opts.WakeTimeout = DefaultWakeTimeout
	}
	return &Client{opts: opts, sessions: map[string]*rpc.Session{}, dialMu: map[string]*sync.Mutex{}, leases: map[string]map[string]bool{}}
}

// SetWaker installs the waker after construction (the controller and the
// client reference each other).
func (c *Client) SetWaker(w Waker) { c.opts.Waker = w }

// SetLeaser installs the leaser after construction.
func (c *Client) SetLeaser(l Leaser) { c.opts.Leaser = l }

// ErrUnavailable wraps failures to reach the guest daemon.
var ErrUnavailable = errors.New("guest daemon unavailable")

func (c *Client) session(ctx context.Context, tenantID string, wake bool) (*rpc.Session, error) {
	if wake && c.opts.Waker != nil {
		wctx, cancel := context.WithTimeout(ctx, c.opts.WakeTimeout)
		err := c.opts.Waker.EnsureRunning(wctx, tenantID)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	if s := c.sessions[tenantID]; s != nil && !s.IsClosed() {
		c.mu.Unlock()
		return s, nil
	}
	dm := c.dialMu[tenantID]
	if dm == nil {
		dm = &sync.Mutex{}
		c.dialMu[tenantID] = dm
	}
	c.mu.Unlock()

	dm.Lock()
	defer dm.Unlock()
	c.mu.Lock()
	if s := c.sessions[tenantID]; s != nil && !s.IsClosed() {
		c.mu.Unlock()
		return s, nil
	}
	c.mu.Unlock()
	conn, err := c.opts.Dialer.DialGuest(ctx, tenantID, rpc.PortGuestRPC)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s, err := rpc.NewSession(conn)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	c.mu.Lock()
	c.sessions[tenantID] = s
	c.mu.Unlock()
	return s, nil
}

// Reset drops the cached connection of a tenant (after the VM stopped).
func (c *Client) Reset(tenantID string) {
	c.mu.Lock()
	s := c.sessions[tenantID]
	delete(c.sessions, tenantID)
	c.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// transportErr reports errors that mean the connection, not the call, failed.
func transportErr(err error) bool {
	var re *rpc.Error
	if err == nil || errors.As(err, &re) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// do runs fn on the tenant's session, reconnecting once when the cached
// connection turns out to be dead.
func (c *Client) do(ctx context.Context, tenantID string, wake bool, fn func(*rpc.Session) error) error {
	for attempt := 0; ; attempt++ {
		s, err := c.session(ctx, tenantID, wake)
		if err != nil {
			return err
		}
		err = fn(s)
		if attempt == 0 && transportErr(err) {
			c.mu.Lock()
			if c.sessions[tenantID] == s {
				delete(c.sessions, tenantID)
			}
			c.mu.Unlock()
			s.Close()
			continue
		}
		return err
	}
}

func (c *Client) call(ctx context.Context, tenantID string, wake bool, method string, params, out any) error {
	return c.do(ctx, tenantID, wake, func(s *rpc.Session) error { return s.Call(ctx, method, params, out) })
}

// Ping checks the guest daemon without waking the VM.
func (c *Client) Ping(ctx context.Context, tenantID string) (rpc.PingResult, error) {
	var res rpc.PingResult
	err := c.call(ctx, tenantID, false, rpc.MethodPing, nil, &res)
	return res, err
}

// Sync flushes guest filesystems without waking the VM.
func (c *Client) Sync(ctx context.Context, tenantID string) error {
	return c.call(ctx, tenantID, false, rpc.MethodSync, nil, nil)
}

// StartPTY starts a PTY in the guest and attaches to it.
func (c *Client) StartPTY(ctx context.Context, tenantID string, spec PTYSpec) (PTY, error) {
	var info PTYInfo
	params := rpc.PTYStartParams{ID: spec.ID, Command: spec.Command, Dir: spec.Dir, Env: spec.Env, Cols: spec.Cols, Rows: spec.Rows}
	if err := c.call(ctx, tenantID, true, rpc.MethodPTYStart, params, &info); err != nil {
		return nil, err
	}
	c.track(tenantID, info)
	return c.AttachPTY(ctx, tenantID, info.ID)
}

// ListPTYs lists the guest PTYs. It does not wake the VM.
func (c *Client) ListPTYs(ctx context.Context, tenantID string) ([]PTYInfo, error) {
	var res rpc.PTYListResult
	if err := c.call(ctx, tenantID, false, rpc.MethodPTYList, nil, &res); err != nil {
		return nil, err
	}
	for _, info := range res.PTYs {
		c.track(tenantID, info)
	}
	return res.PTYs, nil
}

// WaitPTY blocks until the PTY exits.
func (c *Client) WaitPTY(ctx context.Context, tenantID, id string) (PTYExit, error) {
	var res PTYExit
	err := c.call(ctx, tenantID, false, rpc.MethodPTYWait, rpc.PTYIDParams{ID: id}, &res)
	return res, err
}

// SignalPTY signals a PTY's foreground process group.
func (c *Client) SignalPTY(ctx context.Context, tenantID, id, sig string) error {
	return c.call(ctx, tenantID, false, rpc.MethodPTYSignal, rpc.PTYSignalParams{ID: id, Signal: sig}, nil)
}

// ResizePTY resizes a PTY.
func (c *Client) ResizePTY(ctx context.Context, tenantID, id string, cols, rows uint16) error {
	return c.call(ctx, tenantID, false, rpc.MethodPTYResize, rpc.PTYResizeParams{ID: id, Cols: cols, Rows: rows}, nil)
}

// KillPTY terminates a PTY (SIGTERM, SIGKILL after 3 s) and forgets it.
func (c *Client) KillPTY(ctx context.Context, tenantID, id string) error {
	return c.call(ctx, tenantID, false, rpc.MethodPTYKill, rpc.PTYIDParams{ID: id}, nil)
}

// ListTools returns the guest Computer Use MCP tools (tools/list result).
func (c *Client) ListTools(ctx context.Context, tenantID string) (json.RawMessage, error) {
	var res json.RawMessage
	err := c.call(ctx, tenantID, true, rpc.MethodToolsList, nil, &res)
	return res, err
}

// CallTool relays an MCP tools/call to the guest and returns the MCP
// CallToolResult object.
func (c *Client) CallTool(ctx context.Context, tenantID, name string, args json.RawMessage) (json.RawMessage, error) {
	var res json.RawMessage
	err := c.call(ctx, tenantID, true, rpc.MethodToolCall, rpc.ToolCallParams{Name: name, Arguments: args}, &res)
	return res, err
}

// GetMode returns the desktop control mode (does not wake the VM).
func (c *Client) GetMode(ctx context.Context, tenantID string) (ModeState, error) {
	var st ModeState
	err := c.call(ctx, tenantID, false, rpc.MethodModeGet, nil, &st)
	return st, err
}

// SetMode changes the desktop control mode; by is user, agent, or system.
func (c *Client) SetMode(ctx context.Context, tenantID, mode, by string) (ModeState, error) {
	var st ModeState
	err := c.call(ctx, tenantID, false, rpc.MethodModeSet, rpc.ModeSetParams{Mode: mode, By: by}, &st)
	return st, err
}

// LaunchApp launches chrome, blender, or terminal on the guest desktop.
func (c *Client) LaunchApp(ctx context.Context, tenantID, app string) error {
	return c.call(ctx, tenantID, true, rpc.MethodDesktopLaunch, rpc.LaunchParams{App: app}, nil)
}

// track takes a pty lease for a running PTY until it exits.
func (c *Client) track(tenantID string, info PTYInfo) {
	if c.opts.Leaser == nil || !info.Running {
		return
	}
	c.mu.Lock()
	if c.leases[tenantID] == nil {
		c.leases[tenantID] = map[string]bool{}
	}
	if c.leases[tenantID][info.ID] {
		c.mu.Unlock()
		return
	}
	c.leases[tenantID][info.ID] = true
	c.mu.Unlock()
	release := c.opts.Leaser.AcquireLease(tenantID, LeasePTY)
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.leases[tenantID], info.ID)
			c.mu.Unlock()
			release()
		}()
		for failures := 0; failures < 3; {
			_, err := c.WaitPTY(context.Background(), tenantID, info.ID)
			if err == nil || !transportErr(err) {
				return
			}
			failures++
			time.Sleep(2 * time.Second)
		}
	}()
}
