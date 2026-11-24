//go:build unix

package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/portwatcher/lydiabox/internal/guest/credproxy"
	guestlink "github.com/portwatcher/lydiabox/internal/guest/link"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// linkHostAddr is the default LYDIABOX_HOST_ADDR in link mode: a local
// UNIX socket that the daemon bridges to host port 9001 through the link,
// so that child processes (the git credential helper) reach the broker.
const linkHostAddr = rpc.HostAddrDocker

// linkDefaults sets the link-mode default of -host-addr when neither the
// flag nor LYDIABOX_HOST_ADDR set it.
func linkDefaults(f *serveFlags, set map[string]bool) {
	if guestlink.IsListenAddr(f.listen) && !set["host-addr"] && os.Getenv(rpc.EnvHostAddr) == "" {
		f.hostAddr = linkHostAddr
	}
}

// listenRPC returns the RPC listener. A vsock:// or unix:// address
// listens directly. A ws:// address serves the host link on TCP: the
// listener accepts the RPC streams of the link, and the host addresses
// become local UNIX sockets bridged to host ports 9001 and 9002.
func listenRPC(ctx context.Context, f *serveFlags, log *slog.Logger, run func(string, func() error)) (net.Listener, error) {
	if !guestlink.IsListenAddr(f.listen) {
		if _, err := rpc.ParseAddr(f.hostAddr); err != nil {
			return nil, err
		}
		return rpc.Listen(f.listen)
	}
	la, err := guestlink.ParseListenAddr(f.listen)
	if err != nil {
		return nil, err
	}
	brokerPath, err := unixPath(f.hostAddr, "-host-addr")
	if err != nil {
		return nil, err
	}
	gatewayPath, err := unixPath(f.gatewayAddr, "-gateway-addr")
	if err != nil {
		return nil, err
	}
	secret, err := guestlink.SecretFromEnv()
	if err != nil {
		return nil, err
	}
	g, err := guestlink.NewGuest(guestlink.GuestOptions{Secret: secret, Logger: log})
	if err != nil {
		return nil, err
	}
	ln, err := g.Listen(rpc.PortGuestRPC)
	if err != nil {
		g.Close()
		return nil, err
	}
	tcp, err := net.Listen("tcp", la.HostPort)
	if err != nil {
		g.Close()
		return nil, fmt.Errorf("listen for the host link on %s: %w", la.HostPort, err)
	}
	var bridges []net.Listener
	for _, b := range []struct {
		path string
		port uint32
	}{{brokerPath, rpc.PortHostBroker}, {gatewayPath, rpc.PortHostGateway}} {
		bl, err := listenBridge(b.path)
		if err != nil {
			tcp.Close()
			for _, x := range bridges {
				x.Close()
			}
			g.Close()
			return nil, err
		}
		bridges = append(bridges, bl)
		port := b.port
		run(fmt.Sprintf("link bridge %d", port), func() error {
			return credproxy.ServeTunnel(ctx, bl, func(ctx context.Context) (net.Conn, error) { return g.Dial(ctx, port) }, log)
		})
	}
	mux := http.NewServeMux()
	mux.Handle(la.Path, g)
	run("host link", func() error {
		defer g.Close()
		return serveHTTPListener(ctx, tcp, mux)
	})
	log.Info("waiting for the host link", "listen", f.listen)
	return ln, nil
}

func unixPath(addr, flag string) (string, error) {
	a, err := rpc.ParseAddr(addr)
	if err != nil {
		return "", err
	}
	if a.Network != "unix" {
		return "", fmt.Errorf("%s must be a unix:// address in link mode (the daemon bridges it to the host link), got %q", flag, addr)
	}
	return a.Path, nil
}

// listenBridge listens on a local UNIX socket that every guest user can
// reach, as the docker driver does with its shared socket directory.
func listenBridge(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return rpc.ListenUnix(path, 0o666)
}
