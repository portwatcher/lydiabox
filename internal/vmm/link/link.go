// Package link is the host end of the guest link (internal/guest/link): it
// dials one WebSocket URL of a remote machine, keeps the link up with
// reconnects and backoff, and implements the DialGuest and ListenGuestPort
// methods of vmm.Driver on top of it. A remote VM driver keeps one Link for
// each tenant (see Links).
package link

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	guestlink "github.com/lydiabox/lydiabox/internal/guest/link"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// State is the state of a link.
type State string

// Link states.
const (
	// StateIdle: not connecting (before the first Connect, or after
	// Suspend). Calls fail with vmm.ErrUnavailable.
	StateIdle State = "idle"
	// StateWaking: the first attempt after Connect or Wake. Calls wait for
	// it, up to WakeTimeout, because a paused machine wakes on this request.
	StateWaking State = "waking"
	// StateUp: the link is connected and authenticated.
	StateUp State = "up"
	// StateDown: the link failed; it reconnects with backoff. Calls fail
	// at once with vmm.ErrUnavailable.
	StateDown State = "down"
	// StateClosed: Close was called.
	StateClosed State = "closed"
)

// Defaults.
const (
	DefaultWakeTimeout = 60 * time.Second
	DefaultMinBackoff  = 500 * time.Millisecond
	DefaultMaxBackoff  = 30 * time.Second
)

// Target is the WebSocket endpoint of the guest and the extra headers of
// the upgrade request (for example the provider access token).
type Target struct {
	URL    string
	Header http.Header
}

// Options configures a Link.
type Options struct {
	// Secret is the link secret that the guest daemon also has.
	Secret []byte
	// WakeTimeout bounds one connection attempt, including the wake of a
	// paused machine (default 60 s).
	WakeTimeout time.Duration
	// MinBackoff and MaxBackoff bound the wait between reconnects.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// BootTimeout is how long failed attempts are expected after the link
	// starts or loses its session, while the machine boots and the guest
	// daemon starts (default vmm.DefaultBootTimeout). They are logged at
	// info level; a link that is still down after BootTimeout is logged
	// once at warning level with the last cause.
	BootTimeout time.Duration
	// KeepAlive is the yamux ping interval (default 10 s). A link without
	// an answer within three intervals is down.
	KeepAlive time.Duration
	// HTTPClient dials the WebSocket (default http.DefaultClient).
	HTTPClient *http.Client
	// OnState is called with the current status after state changes,
	// outside locks. It must not block for long.
	OnState func(Status)
	Logger  *slog.Logger
}

// Status is a snapshot of the link.
type Status struct {
	State State
	// LastError is the error of the last failed attempt or of the last
	// session end.
	LastError string
	// Since is the time of the last state change.
	Since time.Time
	// Connects counts the successful connections.
	Connects int
}

// Link is the host end of one guest link.
type Link struct {
	opts  Options
	ports *guestlink.Ports

	mu      sync.Mutex
	target  *Target
	status  Status
	sess    *yamux.Session
	changed chan struct{} // closed and replaced on each state change
	cancel  context.CancelFunc
	done    chan struct{} // closed when the run loop exits
	kick    chan struct{}
	attempt time.Time
	notify  bool
}

// New returns an idle link. Call Connect to start it.
func New(opts Options) (*Link, error) {
	if err := guestlink.ValidSecret(opts.Secret); err != nil {
		return nil, err
	}
	if opts.WakeTimeout <= 0 {
		opts.WakeTimeout = DefaultWakeTimeout
	}
	if opts.BootTimeout <= 0 {
		opts.BootTimeout = vmm.DefaultBootTimeout
	}
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = DefaultMinBackoff
	}
	if opts.MaxBackoff < opts.MinBackoff {
		opts.MaxBackoff = max(DefaultMaxBackoff, opts.MinBackoff)
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = http.DefaultClient
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Link{
		opts: opts, ports: guestlink.NewPorts(nil, opts.Logger),
		status:  Status{State: StateIdle, Since: time.Now()},
		changed: make(chan struct{}), kick: make(chan struct{}, 1),
	}, nil
}

// Connect sets the target and wakes the link. A new target replaces the
// current link.
func (l *Link) Connect(t Target) error {
	if t.URL == "" {
		return errors.New("link: target URL is required")
	}
	l.mu.Lock()
	if l.status.State == StateClosed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	t.Header = t.Header.Clone()
	l.target = &t
	l.mu.Unlock()
	l.Suspend()
	return l.Wake()
}

// Wake starts connecting when the link is idle, and retries at once when
// it is down. Calls made during the first attempt wait for it.
func (l *Link) Wake() error {
	l.mu.Lock()
	defer l.unlock()
	switch {
	case l.status.State == StateClosed:
		return net.ErrClosed
	case l.target == nil:
		return errors.New("link: no target; call Connect first")
	case l.done != nil:
		if l.status.State == StateDown {
			l.setStateLocked(StateWaking, l.status.LastError)
			select {
			case l.kick <- struct{}{}:
			default:
			}
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel, l.done = cancel, make(chan struct{})
	l.setStateLocked(StateWaking, "")
	go l.run(ctx, *l.target, l.done)
	return nil
}

// Suspend closes the link and stops reconnecting, for example before the
// provider pauses the machine: a reconnect would wake it again. It returns
// after the run loop has exited.
func (l *Link) Suspend() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel, l.done = nil, nil
	l.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	l.mu.Lock()
	if l.status.State != StateClosed {
		l.setStateLocked(StateIdle, l.status.LastError)
	}
	l.unlock()
}

// Close ends the link and every listener.
func (l *Link) Close() error {
	l.Suspend()
	l.mu.Lock()
	l.setStateLocked(StateClosed, l.status.LastError)
	l.unlock()
	l.ports.Close()
	return nil
}

// Status returns a snapshot of the link.
func (l *Link) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Listen returns a listener for streams that the guest opens to a host
// port. It can be called before Connect, and it survives reconnects.
func (l *Link) Listen(port uint32) (net.Listener, error) { return l.ports.Listen(port) }

// Dial opens a stream to a guest port. While the link wakes it waits, up
// to ctx; in any other state than up it fails at once with an error that
// wraps vmm.ErrUnavailable.
func (l *Link) Dial(ctx context.Context, port uint32) (net.Conn, error) {
	for {
		l.mu.Lock()
		st, sess, changed := l.status, l.sess, l.changed
		if st.State == StateDown {
			l.kickLocked()
		}
		l.mu.Unlock()
		switch st.State {
		case StateUp:
			c, err := guestlink.OpenStream(sess, port)
			if err == nil {
				return c, nil
			}
			return nil, fmt.Errorf("%w: guest link: %v", vmm.ErrUnavailable, err)
		case StateWaking:
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: guest link is waking: %v", vmm.ErrUnavailable, ctx.Err())
			}
		default:
			msg := fmt.Sprintf("guest link is %s", st.State)
			if st.LastError != "" {
				msg += ": " + st.LastError
			}
			return nil, fmt.Errorf("%w: %s", vmm.ErrUnavailable, msg)
		}
	}
}

// kickLocked asks a run loop in backoff to retry now, at most once per
// MinBackoff.
func (l *Link) kickLocked() {
	if time.Since(l.attempt) < l.opts.MinBackoff {
		return
	}
	select {
	case l.kick <- struct{}{}:
	default:
	}
}

func (l *Link) setStateLocked(s State, lastErr string) {
	if l.status.State == s && l.status.LastError == lastErr {
		return
	}
	l.status.State, l.status.LastError, l.status.Since = s, lastErr, time.Now()
	close(l.changed)
	l.changed = make(chan struct{})
	l.notify = true
}

// unlock releases mu and calls OnState with the current status when the
// state changed. Under concurrent changes OnState can skip a state, but
// the last call reports the last state.
func (l *Link) unlock() {
	n := l.notify
	l.notify = false
	l.mu.Unlock()
	if n && l.opts.OnState != nil {
		l.opts.OnState(l.Status())
	}
}
