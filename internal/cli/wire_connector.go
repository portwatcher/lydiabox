package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/connector"
	"github.com/lydiabox/lydiabox/internal/connector/broker"
	"github.com/lydiabox/lydiabox/internal/version"
)

// connectorBrokers maps the deps of a running `lydiabox up` to the connector
// broker that wireConnector built. The VM wiring serves it on the guest's
// vsock port: connectorBroker(d).Serve(ctx, ln) with ln from
// driver.ListenGuestPort(ctx, tenantID, config.HostBrokerPort).
var connectorBrokers sync.Map // *deps -> *broker.Broker

// connectorBroker returns the broker built for d, or nil before wireConnector ran.
func connectorBroker(d *deps) *broker.Broker {
	if b, ok := connectorBrokers.Load(d); ok {
		return b.(*broker.Broker)
	}
	return nil
}

// wireConnector wires Phase 4 (connectors) into `lydiabox up`: the connector
// manager behind /api/v1/connectors, and the guest-facing broker (git-cred
// and the connector MCP server). The broker is served on
// $DATA_DIR/run/host-9001.sock, which the docker driver mounts into the
// guest and which serves development without a VM.
func wireConnector(_ context.Context, d *deps) (*component, error) {
	clients := map[string]connector.OAuthClient{}
	for p, c := range d.Config.OAuthClients {
		clients[p] = connector.OAuthClient{ClientID: c.ClientID, ClientSecret: c.ClientSecret}
	}
	mgr, err := connector.New(connector.Options{
		Store: d.Store, EnvClients: clients, RelayURL: d.Config.OAuthRelayURL, Logger: d.Logger,
	})
	if err != nil {
		return nil, err
	}
	brk, err := broker.New(broker.Options{
		Manager: mgr, TenantID: d.Config.TenantID, Dirs: d.Storage, Logger: d.Logger,
		ServerVersion: version.Get().Version,
	})
	if err != nil {
		return nil, err
	}
	connectorBrokers.Store(d, brk)

	sock := d.Config.HostBrokerSocket()
	var (
		cancel context.CancelFunc
		done   chan error
	)
	return &component{
		Name:   "connector",
		Routes: []api.RouteRegistrar{api.ConnectorRoutes{Manager: mgr, Logger: d.Logger}},
		Start: func(ctx context.Context) error {
			ln, err := listenUnix(sock)
			if err != nil {
				return err
			}
			var sctx context.Context
			sctx, cancel = context.WithCancel(ctx)
			done = make(chan error, 1)
			go func() { done <- brk.Serve(sctx, ln) }()
			d.Logger.Info("connector broker listening", "socket", sock)
			return nil
		},
		Stop: func(ctx context.Context) error {
			defer connectorBrokers.Delete(d)
			if cancel == nil {
				return nil
			}
			cancel()
			select {
			case err := <-done:
				_ = os.Remove(sock)
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}, nil
}

// listenUnix listens on a 0600 UNIX socket, replacing a stale socket file.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("connector broker: %w", err)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, fmt.Errorf("connector broker: %s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("connector broker: remove stale socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("connector broker: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("connector broker: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("connector broker: secure %s: %w", path, err)
	}
	return ln, nil
}
