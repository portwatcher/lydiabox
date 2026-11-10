package rpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Well-known vsock ports (docs/design.md section 4.3).
const (
	// PortGuestRPC is the guest daemon RPC port (host to guest).
	PortGuestRPC uint32 = 9000
	// PortHostBroker is the host connector broker (guest to host, HTTP).
	PortHostBroker uint32 = 9001
	// PortHostGateway is the host LLM gateway tunnel (guest to host).
	PortHostGateway uint32 = 9002
	// HostCID is the vsock context ID of the host.
	HostCID uint32 = 2
	// GuestCID is the vsock context ID assigned to the guest VM.
	GuestCID uint32 = 3
	// AnyCID listens on every local context ID.
	AnyCID uint32 = 0xFFFFFFFF
)

// Addr is a parsed transport address: vsock://<cid>:<port>, vsock://:<port>
// (listen on any CID), or unix:///absolute/path.
type Addr struct {
	Network string // "vsock" or "unix"
	CID     uint32
	Port    uint32
	Path    string
}

func (a Addr) String() string {
	if a.Network == "unix" {
		return "unix://" + a.Path
	}
	if a.CID == AnyCID {
		return fmt.Sprintf("vsock://:%d", a.Port)
	}
	return fmt.Sprintf("vsock://%d:%d", a.CID, a.Port)
}

// ParseAddr parses a transport address.
func ParseAddr(s string) (Addr, error) {
	switch {
	case strings.HasPrefix(s, "unix://"):
		p := strings.TrimPrefix(s, "unix://")
		if p == "" || !strings.HasPrefix(p, "/") {
			return Addr{}, fmt.Errorf("rpc: unix address %q needs an absolute path", s)
		}
		return Addr{Network: "unix", Path: p}, nil
	case strings.HasPrefix(s, "vsock://"):
		rest := strings.TrimPrefix(s, "vsock://")
		host, port, ok := strings.Cut(rest, ":")
		if !ok {
			return Addr{}, fmt.Errorf("rpc: vsock address %q needs <cid>:<port>", s)
		}
		p, err := strconv.ParseUint(port, 10, 32)
		if err != nil {
			return Addr{}, fmt.Errorf("rpc: vsock address %q: invalid port", s)
		}
		cid := AnyCID
		if host != "" {
			c, err := strconv.ParseUint(host, 10, 32)
			if err != nil {
				return Addr{}, fmt.Errorf("rpc: vsock address %q: invalid cid", s)
			}
			cid = uint32(c)
		}
		return Addr{Network: "vsock", CID: cid, Port: uint32(p)}, nil
	default:
		return Addr{}, fmt.Errorf("rpc: unsupported address %q (use vsock:// or unix://)", s)
	}
}

// ErrVsockUnsupported is returned on platforms without AF_VSOCK.
var ErrVsockUnsupported = errors.New("rpc: AF_VSOCK is only supported on Linux")

// Dial connects to a vsock or UNIX address. The guest uses it to reach the
// host broker (LYDIABOX_HOST_ADDR) and the LLM gateway tunnel.
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	a, err := ParseAddr(addr)
	if err != nil {
		return nil, err
	}
	if a.Network == "unix" {
		var d net.Dialer
		return d.DialContext(ctx, "unix", a.Path)
	}
	if a.CID == AnyCID {
		return nil, fmt.Errorf("rpc: dial %q: a vsock dial needs a cid", addr)
	}
	return dialVsock(ctx, a.CID, a.Port)
}

// Listen listens on a vsock or UNIX address. A stale UNIX socket file is
// replaced, and the socket is made accessible to other local users (the
// docker driver shares it across a bind mount with a different uid).
func Listen(addr string) (net.Listener, error) {
	a, err := ParseAddr(addr)
	if err != nil {
		return nil, err
	}
	if a.Network == "unix" {
		return ListenUnix(a.Path, 0o666)
	}
	return listenVsock(a.CID, a.Port)
}

// ListenUnix listens on path, replacing a stale socket file, and sets mode.
func ListenUnix(path string, mode os.FileMode) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("rpc: %s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// SubAddr returns the address of another port on the same peer: for vsock the
// port changes, for UNIX sockets the file host-<port>.sock in the same
// directory. It derives the gateway address from LYDIABOX_HOST_ADDR.
func SubAddr(addr string, port uint32) (string, error) {
	a, err := ParseAddr(addr)
	if err != nil {
		return "", err
	}
	if a.Network == "vsock" {
		a.Port = port
		return a.String(), nil
	}
	dir := a.Path[:strings.LastIndex(a.Path, "/")+1]
	return fmt.Sprintf("unix://%shost-%d.sock", dir, port), nil
}
