//go:build linux

package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// VsockAddr is a vsock endpoint.
type VsockAddr struct {
	CID  uint32
	Port uint32
}

// Network implements net.Addr.
func (VsockAddr) Network() string { return "vsock" }

func (a VsockAddr) String() string { return fmt.Sprintf("vsock://%d:%d", a.CID, a.Port) }

func vsockSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return -1, fmt.Errorf("rpc: vsock socket: %w", err)
	}
	return fd, nil
}

func listenVsock(cid, port uint32) (net.Listener, error) {
	fd, err := vsockSocket()
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: cid, Port: port}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("rpc: vsock bind %d: %w", port, err)
	}
	if err := unix.Listen(fd, 128); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("rpc: vsock listen %d: %w", port, err)
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock-listener:%d", port))
	return &vsockListener{f: f, addr: VsockAddr{CID: cid, Port: port}}, nil
}

type vsockListener struct {
	f    *os.File
	addr VsockAddr
}

func (l *vsockListener) Accept() (net.Conn, error) {
	rc, err := l.f.SyscallConn()
	if err != nil {
		return nil, err
	}
	var (
		nfd    int
		sa     unix.Sockaddr
		accErr error
	)
	err = rc.Read(func(fd uintptr) bool {
		nfd, sa, accErr = unix.Accept4(int(fd), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
		// Transient failures wait for the next connection instead of
		// ending the accept loop.
		return !errors.Is(accErr, unix.EAGAIN) && !errors.Is(accErr, unix.EINTR) && !errors.Is(accErr, unix.ECONNABORTED)
	})
	if err != nil {
		return nil, err
	}
	if accErr != nil {
		return nil, accErr
	}
	remote := VsockAddr{}
	if vm, ok := sa.(*unix.SockaddrVM); ok {
		remote = VsockAddr{CID: vm.CID, Port: vm.Port}
	}
	return newVsockConn(nfd, l.addr, remote), nil
}

func (l *vsockListener) Close() error   { return l.f.Close() }
func (l *vsockListener) Addr() net.Addr { return l.addr }

func dialVsock(ctx context.Context, cid, port uint32) (net.Conn, error) {
	fd, err := vsockSocket()
	if err != nil {
		return nil, err
	}
	err = unix.Connect(fd, &unix.SockaddrVM{CID: cid, Port: port})
	if err != nil && !errors.Is(err, unix.EINPROGRESS) {
		unix.Close(fd)
		return nil, fmt.Errorf("rpc: vsock connect %d:%d: %w", cid, port, err)
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("vsock:%d:%d", cid, port))
	if errors.Is(err, unix.EINPROGRESS) {
		if dl, ok := ctx.Deadline(); ok {
			_ = f.SetWriteDeadline(dl)
		}
		stop := context.AfterFunc(ctx, func() { _ = f.SetWriteDeadline(time.Unix(1, 0)) })
		rc, rerr := f.SyscallConn()
		if rerr != nil {
			stop()
			f.Close()
			return nil, rerr
		}
		var soErr error
		werr := rc.Write(func(fd uintptr) bool {
			v, gerr := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_ERROR)
			if gerr != nil {
				soErr = gerr
				return true
			}
			if v != 0 {
				soErr = syscall.Errno(v)
			}
			return true
		})
		stop()
		_ = f.SetWriteDeadline(time.Time{})
		if werr != nil || soErr != nil {
			f.Close()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("rpc: vsock connect %d:%d: %w", cid, port, errors.Join(werr, soErr))
		}
	}
	local := VsockAddr{}
	if sa, err := unix.Getsockname(fd); err == nil {
		if vm, ok := sa.(*unix.SockaddrVM); ok {
			local = VsockAddr{CID: vm.CID, Port: vm.Port}
		}
	}
	return &vsockConn{f: f, local: local, remote: VsockAddr{CID: cid, Port: port}}, nil
}

func newVsockConn(fd int, local, remote VsockAddr) *vsockConn {
	return &vsockConn{f: os.NewFile(uintptr(fd), "vsock-conn"), local: local, remote: remote}
}

// vsockConn is a net.Conn over a non-blocking AF_VSOCK socket. os.File
// integrates the descriptor with the runtime poller, so deadlines work.
type vsockConn struct {
	f             *os.File
	local, remote VsockAddr
}

func (c *vsockConn) Read(b []byte) (int, error)         { return c.f.Read(b) }
func (c *vsockConn) Write(b []byte) (int, error)        { return c.f.Write(b) }
func (c *vsockConn) Close() error                       { return c.f.Close() }
func (c *vsockConn) LocalAddr() net.Addr                { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr               { return c.remote }
func (c *vsockConn) SetDeadline(t time.Time) error      { return c.f.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return c.f.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return c.f.SetWriteDeadline(t) }

// CloseWrite shuts down the sending side (used by byte-level proxies).
func (c *vsockConn) CloseWrite() error {
	rc, err := c.f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = unix.Shutdown(int(fd), unix.SHUT_WR) }); err != nil {
		return err
	}
	return serr
}
