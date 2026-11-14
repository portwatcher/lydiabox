// Package desktop is the host side of desktop streaming: the WebSocket
// signaling relay between browsers and the guest WebRTC peer
// (/api/v1/desktop/ws, docs/api.md section 9), the takeover mode API, the
// broadcaster of agent_action and mode events to every connected browser,
// and the desktop lease that keeps the VM awake while someone watches.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/lydiabox/lydiabox/internal/desktop/ice"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// Subprotocol is the WebSocket subprotocol of the signaling socket.
const Subprotocol = "lydiabox.desktop.v1"

// LeaseDesktop is the controller lease kind held per open socket.
const LeaseDesktop = "desktop"

// SignalConn is one viewer's signaling stream to the guest.
type SignalConn interface {
	Send(msg json.RawMessage) error
	Recv() (json.RawMessage, error)
	Close() error
}

// EventConn is the guest desktop event stream.
type EventConn interface {
	Recv() (json.RawMessage, error)
	Close() error
}

// Guest is the guest daemon as seen by the relay.
type Guest interface {
	OpenSignal(ctx context.Context, tenantID string, p rpc.SignalParams) (SignalConn, rpc.SignalHello, error)
	SubscribeEvents(ctx context.Context, tenantID string) (EventConn, error)
	GetMode(ctx context.Context, tenantID string) (rpc.ModeState, error)
	SetMode(ctx context.Context, tenantID, mode, by string) (rpc.ModeState, error)
	LaunchApp(ctx context.Context, tenantID, app string) error
}

// Power wakes the VM and holds leases (vmm.Controller).
type Power interface {
	EnsureRunning(ctx context.Context, tenantID string) error
	AcquireLease(tenantID, kind string) (release func())
}

// ICEServer is an entry of the hello message's iceServers.
type ICEServer = rpc.ICEServer

// Options configures a Relay.
type Options struct {
	Guest Guest
	Power Power
	// Running reports whether the VM runs (mode routes answer 503 otherwise).
	Running func(tenantID string) bool
	// PublicIP returns the address at which the browser that sent r reaches
	// the forwarded WebRTC UDP port ("" lets the guest advertise its own).
	PublicIP func(r *http.Request) string
	// ICEServers are sent to the browser in hello when ICE is nil, and when
	// ICE fails outside relay-only mode (default: Google STUN).
	ICEServers []ICEServer
	// ICE, when set, gives each viewer its ICE servers, which go to the
	// browser in hello and to the guest peer in the signaling parameters.
	// Credentials that it mints are replaced before they expire. When ICE
	// is nil, the guest gets no ICE servers.
	ICE ice.Provider
	// RelayOnly sets the ICE transport policy "relay" on both peers.
	RelayOnly bool
	// WakeTimeout bounds waking the VM for a new socket (default 120 s).
	WakeTimeout time.Duration
	Logger      *slog.Logger
}

// Relay serves the desktop routes.
type Relay struct {
	opts Options

	mu   sync.Mutex
	hubs map[string]*hub
}

// New returns a relay.
func New(opts Options) (*Relay, error) {
	if opts.Guest == nil || opts.Power == nil {
		return nil, errors.New("desktop: Guest and Power are required")
	}
	if opts.Running == nil {
		opts.Running = func(string) bool { return true }
	}
	if opts.PublicIP == nil {
		opts.PublicIP = func(*http.Request) string { return "" }
	}
	if opts.ICEServers == nil {
		opts.ICEServers = []ICEServer{{URLs: []string{ice.DefaultSTUNURL}}}
	}
	if opts.RelayOnly && opts.ICE == nil {
		return nil, errors.New("desktop: RelayOnly needs an ICE provider with a TURN server")
	}
	if opts.WakeTimeout <= 0 {
		opts.WakeTimeout = 120 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Relay{opts: opts, hubs: map[string]*hub{}}, nil
}

// Viewers returns the number of open sockets of a tenant.
func (rl *Relay) Viewers(tenantID string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if h := rl.hubs[tenantID]; h != nil {
		return len(h.clients)
	}
	return 0
}

// Server -> browser messages.
type helloMsg struct {
	Type               string      `json:"type"`
	Mode               string      `json:"mode"`
	Screen             rpc.Screen  `json:"screen"`
	ICEServers         []ICEServer `json:"iceServers"`
	ICETransportPolicy string      `json:"iceTransportPolicy"`
}

type errorMsg struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type viewersMsg struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type pongMsg struct {
	Type string          `json:"type"`
	ID   json.RawMessage `json:"id"`
}

// clientMsg is any browser -> server message.
type clientMsg struct {
	Type string          `json:"type"`
	Mode string          `json:"mode"`
	App  string          `json:"app"`
	ID   json.RawMessage `json:"id"`
}

// ServeWS handles GET /api/v1/desktop/ws.
func (rl *Relay) ServeWS(w http.ResponseWriter, r *http.Request) {
	tid := tenant.FromContext(r.Context())
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		return
	}
	if conn.Subprotocol() != Subprotocol {
		conn.Close(websocket.StatusPolicyViolation, "subprotocol "+Subprotocol+" required")
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := newClient(conn, cancel)
	go c.writeLoop(ctx)
	defer c.close(websocket.StatusNormalClosure, "")

	release := rl.opts.Power.AcquireLease(tid, LeaseDesktop)
	defer release()
	wctx, wcancel := context.WithTimeout(ctx, rl.opts.WakeTimeout)
	err = rl.opts.Power.EnsureRunning(wctx, tid)
	wcancel()
	if err != nil {
		c.fail("unavailable", "VM is not running: "+err.Error())
		return
	}
	v := &viewer{rl: rl, tid: tid, c: c, publicIP: rl.opts.PublicIP(r), cancel: cancel}
	if err := v.connect(ctx); err != nil {
		c.fail("unavailable", "desktop stream failed to start: "+err.Error())
		return
	}
	defer v.close()
	rl.join(tid, c)
	defer rl.leave(tid, c)
	go v.refreshLoop(ctx)
	rl.readLoop(ctx, tid, c, v)
}

func (rl *Relay) readLoop(ctx context.Context, tid string, c *client, sig interface{ Send(json.RawMessage) error }) {
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			c.sendJSON(errorMsg{Type: "error", Code: "bad_request", Message: "text frames only"})
			continue
		}
		var m clientMsg
		if err := json.Unmarshal(data, &m); err != nil {
			c.sendJSON(errorMsg{Type: "error", Code: "bad_request", Message: "invalid JSON message"})
			continue
		}
		switch m.Type {
		case "offer", "ice":
			if err := sig.Send(data); err != nil {
				return
			}
		case "mode":
			if _, err := rl.opts.Guest.SetMode(ctx, tid, m.Mode, "user"); err != nil {
				c.sendJSON(errorMsg{Type: "error", Code: errCode(err), Message: err.Error()})
			}
		case "launch":
			if err := rl.opts.Guest.LaunchApp(ctx, tid, m.App); err != nil {
				c.sendJSON(errorMsg{Type: "error", Code: errCode(err), Message: err.Error()})
			}
		case "ping":
			c.sendJSON(pongMsg{Type: "pong", ID: m.ID})
		default:
			c.sendJSON(errorMsg{Type: "error", Code: "bad_request", Message: "unknown message type " + m.Type})
		}
	}
}

func errCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == rpc.CodeBadRequest {
		return "bad_request"
	}
	return "unavailable"
}

// errMessage returns the message of a guest RPC error without its code
// prefix, which the HTTP error envelope already carries.
func errMessage(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Message
	}
	return err.Error()
}

// client is one browser socket with a serialized writer.
type client struct {
	conn   *websocket.Conn
	out    chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

func newClient(conn *websocket.Conn, cancel context.CancelFunc) *client {
	return &client{conn: conn, out: make(chan []byte, 256), cancel: cancel}
}

// send queues a message; a browser that cannot keep up is disconnected.
func (c *client) send(b []byte) {
	select {
	case c.out <- b:
	default:
		c.close(websocket.StatusPolicyViolation, "client too slow")
	}
}

func (c *client) sendJSON(v any) {
	b, err := json.Marshal(v)
	if err == nil {
		c.send(b)
	}
}

func (c *client) fail(code, msg string) {
	c.sendJSON(errorMsg{Type: "error", Code: code, Message: msg})
	// Give the writer a moment to deliver the error before closing.
	time.Sleep(100 * time.Millisecond)
	c.close(websocket.StatusInternalError, msg)
}

func (c *client) writeLoop(ctx context.Context) {
	for {
		select {
		case b := <-c.out:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *client) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		c.cancel()
		if len(reason) > 120 {
			reason = reason[:120]
		}
		_ = c.conn.Close(code, reason)
	})
}
