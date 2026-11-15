package link

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// DefaultDialWait bounds how long a guest dial waits for a host link.
const DefaultDialWait = 30 * time.Second

// GuestOptions configures a Guest.
type GuestOptions struct {
	// Secret is the link secret (at least MinSecretLength bytes).
	Secret []byte
	// DialWait bounds how long Dial waits for a link when none is up
	// (default DefaultDialWait). A paused machine resumes before the host
	// reconnects, so a request made at that moment waits for the link.
	DialWait time.Duration
	// KeepAlive is the yamux ping interval (default 10 s).
	KeepAlive time.Duration
	Logger    *slog.Logger
}

// Guest is the guest end of the link: an http.Handler that accepts the
// WebSocket of the host. It keeps one link; a new authenticated link
// replaces the old one, because the host reconnects after a network
// failure that the guest may not have seen.
type Guest struct {
	opts  GuestOptions
	ports *Ports
	ctx   context.Context
	stop  context.CancelFunc

	mu      sync.Mutex
	sess    *yamux.Session
	changed chan struct{} // closed and replaced when sess changes
	wg      sync.WaitGroup
}

// NewGuest returns a guest link endpoint.
func NewGuest(opts GuestOptions) (*Guest, error) {
	if err := ValidSecret(opts.Secret); err != nil {
		return nil, err
	}
	if opts.DialWait <= 0 {
		opts.DialWait = DefaultDialWait
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Guest{opts: opts, ports: NewPorts(nil, opts.Logger), ctx: ctx, stop: stop, changed: make(chan struct{})}, nil
}

// Listen returns the listener of a guest port (the RPC listens on 9000).
func (g *Guest) Listen(port uint32) (net.Listener, error) { return g.ports.Listen(port) }

// Connected reports whether a host link is up.
func (g *Guest) Connected() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sess != nil && !g.sess.IsClosed()
}

// Dial opens a stream to a host port. Without a link it waits up to
// DialWait for the host to connect.
func (g *Guest) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, g.opts.DialWait)
	defer cancel()
	for {
		g.mu.Lock()
		sess, changed := g.sess, g.changed
		g.mu.Unlock()
		if sess != nil && !sess.IsClosed() {
			c, err := OpenStream(sess, port)
			if err == nil {
				return c, nil
			}
			if !sess.IsClosed() {
				return nil, err
			}
		}
		var closed <-chan struct{}
		if sess != nil {
			closed = sess.CloseChan()
		}
		select {
		case <-changed:
		case <-closed:
			// Wait for the next link.
			select {
			case <-changed:
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: %v", errNoSession, ctx.Err())
			case <-g.ctx.Done():
				return nil, net.ErrClosed
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", errNoSession, ctx.Err())
		case <-g.ctx.Done():
			return nil, net.ErrClosed
		}
	}
}

// ServeHTTP accepts the host WebSocket and serves the link until it ends.
func (g *Guest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.ctx.Err() != nil {
		http.Error(w, "closed", http.StatusServiceUnavailable)
		return
	}
	g.wg.Add(1)
	defer g.wg.Done()
	// The handshake authenticates the host; the Origin check of the
	// library does not apply to a non-browser peer behind a proxy.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}, InsecureSkipVerify: true})
	if err != nil {
		return
	}
	hctx, cancel := context.WithTimeout(g.ctx, HandshakeTimeout)
	err = GuestHandshake(hctx, c, g.opts.Secret)
	cancel()
	if err != nil {
		g.opts.Logger.Warn("host link refused", "remote", r.RemoteAddr, "err", err)
		c.Close(CloseUnauthorized, "unauthorized")
		return
	}
	ctx, cancel := context.WithCancel(g.ctx)
	defer cancel()
	sess, err := yamux.Server(websocket.NetConn(ctx, c, websocket.MessageBinary), YamuxConfig(g.opts.KeepAlive))
	if err != nil {
		c.Close(websocket.StatusInternalError, "session")
		return
	}
	g.setSession(sess)
	g.opts.Logger.Info("host link connected", "remote", r.RemoteAddr)
	g.ports.Serve(sess)
	sess.Close()
	g.clearSession(sess)
	g.opts.Logger.Info("host link closed", "remote", r.RemoteAddr)
}

func (g *Guest) setSession(sess *yamux.Session) {
	g.mu.Lock()
	old := g.sess
	g.sess = sess
	close(g.changed)
	g.changed = make(chan struct{})
	g.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (g *Guest) clearSession(sess *yamux.Session) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sess == sess {
		g.sess = nil
		close(g.changed)
		g.changed = make(chan struct{})
	}
}

// Close ends the link and every listener, and waits for the handlers.
func (g *Guest) Close() error {
	g.stop()
	g.mu.Lock()
	sess := g.sess
	g.mu.Unlock()
	if sess != nil {
		sess.Close()
	}
	g.ports.Close()
	g.wg.Wait()
	return nil
}

// ListenAddr is a parsed link listen address: ws://<host>:<port>[/path].
type ListenAddr struct {
	HostPort string
	Path     string
}

// IsListenAddr reports whether s is a link listen address.
func IsListenAddr(s string) bool { return strings.HasPrefix(s, "ws://") }

// ParseListenAddr parses ws://<host>:<port>[/path]. TLS ends at the
// provider proxy, so the guest listens with plain HTTP.
func ParseListenAddr(s string) (ListenAddr, error) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "ws" || u.Host == "" || u.RawQuery != "" || u.User != nil {
		return ListenAddr{}, fmt.Errorf("link: listen address %q must be ws://<host>:<port>[/path]", s)
	}
	if _, port, err := net.SplitHostPort(u.Host); err != nil || port == "" {
		return ListenAddr{}, fmt.Errorf("link: listen address %q needs a port", s)
	}
	p := u.Path
	if p == "" {
		p = "/"
	}
	return ListenAddr{HostPort: u.Host, Path: p}, nil
}

// IsNoLink reports whether err means that no host link was up.
func IsNoLink(err error) bool { return errors.Is(err, errNoSession) }
