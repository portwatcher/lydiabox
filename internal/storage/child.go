package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// childServer supervises one long-running juicefs server process (`juicefs
// webdav`, `juicefs gateway`) that listens on a TCP address. It waits until
// the address accepts connections, restarts the process with exponential
// backoff when it exits, and terminates it on stop.
type childServer struct {
	name         string // "juicefs webdav", used in logs and errors
	log          *slog.Logger
	runner       Runner
	dial         func(ctx context.Context, addr string) error
	readyTimeout time.Duration
	// fixedAddr is the listen address; empty picks a free 127.0.0.1 port on
	// every launch.
	fixedAddr string
	command   func(addr string) Command

	mu       sync.RWMutex
	addr     string
	started  bool
	stopping chan struct{}
	done     chan struct{}
	proc     Process
	watch    *procWatch
}

func defaultDial(ctx context.Context, addr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err == nil {
		c.Close()
	}
	return err
}

// Addr returns the listen address, or "" when the process is not running.
func (c *childServer) Addr() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.addr
}

// start launches the process and its supervisor goroutine.
func (c *childServer) start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("storage: %s already started", c.name)
	}
	if err := c.launch(ctx); err != nil {
		return err
	}
	c.started = true
	c.stopping = make(chan struct{})
	c.done = make(chan struct{})
	go c.supervise()
	return nil
}

// dialAddr turns a wildcard listen address into one that can be dialed.
func dialAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		if ip != nil && ip.To4() == nil {
			return net.JoinHostPort("::1", port)
		}
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// launch must be called with c.mu held.
func (c *childServer) launch(ctx context.Context) error {
	addr := c.fixedAddr
	if addr == "" {
		port, err := freePort()
		if err != nil {
			return fmt.Errorf("storage: %s port: %w", c.name, err)
		}
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	cmd := c.command(addr)
	out := newLogWriter(c.log, c.name)
	cmd.Stdout, cmd.Stderr = out, out
	proc, err := c.runner.Start(cmd)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	watch := watchProcess(proc)
	deadline := time.Now().Add(c.readyTimeout)
	for {
		dctx, cancel := context.WithTimeout(ctx, time.Second)
		err := c.dial(dctx, dialAddr(addr))
		cancel()
		if err == nil {
			break
		}
		select {
		case <-watch.done:
			return fmt.Errorf("storage: %s exited before ready: %v", c.name, watch.err)
		case <-ctx.Done():
			stopProcess(proc, watch)
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			stopProcess(proc, watch)
			return fmt.Errorf("storage: %s not ready on %s after %s", c.name, addr, c.readyTimeout)
		}
	}
	c.addr, c.proc, c.watch = addr, proc, watch
	c.log.Info(c.name+" ready", "addr", addr, "pid", proc.Pid())
	return nil
}

func (c *childServer) supervise() {
	defer close(c.done)
	backoff := time.Second
	for {
		c.mu.RLock()
		watch := c.watch
		c.mu.RUnlock()
		select {
		case <-c.stopping:
			return
		case <-watch.done:
		}
		c.mu.Lock()
		c.addr = ""
		c.mu.Unlock()
		c.log.Error(c.name+" exited; restarting", "err", watch.err, "backoff", backoff)
		select {
		case <-c.stopping:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-c.stopping:
				cancel()
			case <-ctx.Done():
			}
		}()
		c.mu.Lock()
		err := c.launch(ctx)
		c.mu.Unlock()
		cancel()
		if err != nil {
			c.log.Error(c.name+" restart failed", "err", err)
			// Keep a closed watch so the loop retries after the backoff.
			c.mu.Lock()
			c.watch = &procWatch{done: closedChan(), err: err}
			c.mu.Unlock()
			continue
		}
		backoff = time.Second
	}
}

// stop terminates the process and the supervisor.
func (c *childServer) stop(ctx context.Context) error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return nil
	}
	c.started = false
	close(c.stopping)
	c.mu.Unlock()
	select {
	case <-c.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	proc, watch := c.proc, c.watch
	c.addr = ""
	c.mu.Unlock()
	if proc != nil {
		stopProcess(proc, watch)
	}
	return nil
}

func stopProcess(proc Process, w *procWatch) {
	_ = proc.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
	case <-time.After(15 * time.Second):
		_ = proc.Kill()
		<-w.done
	}
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

var errNoBinary = errors.New("storage: Config, Runner, and JuiceFSBinary are required")
