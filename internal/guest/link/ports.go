package link

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
)

// Backlog is the number of accepted streams that wait for Accept on one
// port. A stream that arrives at a full backlog is closed, so a listener
// that does not accept cannot block the session.
const Backlog = 64

// Ports routes the streams that a peer opens to listeners by port. A
// listener stays valid across sessions, so it can be made before the first
// link and keeps working after a reconnect.
type Ports struct {
	addr net.Addr
	log  *slog.Logger

	mu     sync.Mutex
	ports  map[uint32]*Listener
	closed bool
}

// NewPorts returns an empty router. addr is the address that listeners
// report.
func NewPorts(addr net.Addr, log *slog.Logger) *Ports {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Ports{addr: addr, log: log, ports: map[uint32]*Listener{}}
}

// Listen returns the listener of a port. Only one listener can be open for
// each port.
func (p *Ports) Listen(port uint32) (*Listener, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, net.ErrClosed
	}
	if _, dup := p.ports[port]; dup {
		return nil, fmt.Errorf("link: port %d is already listening", port)
	}
	l := &Listener{p: p, port: port, ch: make(chan net.Conn, Backlog), done: make(chan struct{})}
	p.ports[port] = l
	return l, nil
}

// Close closes every listener.
func (p *Ports) Close() {
	p.mu.Lock()
	p.closed = true
	ls := make([]*Listener, 0, len(p.ports))
	for _, l := range p.ports {
		ls = append(ls, l)
	}
	p.mu.Unlock()
	for _, l := range ls {
		_ = l.Close()
	}
}

// Serve accepts the streams of sess until the session ends, and delivers
// each one to the listener of its port.
func (p *Ports) Serve(sess *yamux.Session) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		st, err := sess.AcceptStream()
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.route(st)
		}()
	}
}

func (p *Ports) route(c net.Conn) {
	port, err := readPort(c)
	if err != nil {
		c.Close()
		return
	}
	p.mu.Lock()
	l := p.ports[port]
	p.mu.Unlock()
	if l == nil {
		p.log.Debug("link stream to a port without a listener", "port", port)
		c.Close()
		return
	}
	if !l.deliver(c) {
		p.log.Warn("link stream dropped: listener backlog is full or closed", "port", port)
		c.Close()
	}
}

// Listener is a net.Listener for one link port.
type Listener struct {
	p    *Ports
	port uint32
	ch   chan net.Conn

	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

func (l *Listener) deliver(c net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	select {
	case l.ch <- c:
		return true
	default:
		return false
	}
}

// Accept waits for the next stream to the port.
func (l *Listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close stops the listener and closes the streams that wait for Accept.
func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.done)
	l.mu.Unlock()
	l.p.mu.Lock()
	if l.p.ports[l.port] == l {
		delete(l.p.ports, l.port)
	}
	l.p.mu.Unlock()
	for {
		select {
		case c := <-l.ch:
			c.Close()
		default:
			return nil
		}
	}
}

// Addr reports the link address and the port.
func (l *Listener) Addr() net.Addr { return portAddr{base: l.p.addr, port: l.port} }

type portAddr struct {
	base net.Addr
	port uint32
}

func (a portAddr) Network() string { return "link" }
func (a portAddr) String() string {
	if a.base == nil {
		return fmt.Sprintf("link:%d", a.port)
	}
	return fmt.Sprintf("%s:%d", a.base.String(), a.port)
}

// errNoSession is returned when no link is up.
var errNoSession = errors.New("link: no host link is connected")
