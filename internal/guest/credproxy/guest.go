// Package credproxy bridges in-guest clients to host services without
// putting credentials in the guest:
//
//   - LLM gateway: harnesses talk to http://127.0.0.1:3425 in the guest; every
//     TCP connection is tunneled over vsock port 9002 to the host, where
//     HostGateway injects the magpie gateway key and reverse-proxies to the
//     host magpie gateway.
//   - Connector MCP: http://127.0.0.1:8911/mcp in the guest is proxied over
//     vsock port 9001 to the host connector broker's /mcp endpoint.
package credproxy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// Default guest listen addresses.
const (
	GatewayListenAddr      = "127.0.0.1:3425"
	ConnectorMCPListenAddr = "127.0.0.1:8911"
	// ConnectorMCPPath is the path served by the host broker on port 9001.
	ConnectorMCPPath = "/mcp"
)

// Dialer opens a connection to a host port.
type Dialer func(ctx context.Context) (net.Conn, error)

// AddrDialer dials a vsock:// or unix:// address with rpc.Dial.
func AddrDialer(addr string) Dialer {
	return func(ctx context.Context) (net.Conn, error) { return rpc.Dial(ctx, addr) }
}

// ServeTunnel accepts connections on ln and copies each one, byte for byte,
// to a new connection from dial, until ctx is canceled. HTTP streaming
// (server-sent events) passes through unchanged.
func ServeTunnel(ctx context.Context, ln net.Listener, dial Dialer, log *slog.Logger) error {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			up, err := dial(dctx)
			cancel()
			if err != nil {
				if log != nil {
					log.Warn("gateway tunnel: host unreachable", "err", err)
				}
				c.Close()
				return
			}
			Splice(ctx, c, up)
		}()
	}
}

type closeWriter interface{ CloseWrite() error }

// Splice copies a<->b until both directions finish or ctx ends, half-closing
// each side as the other reaches EOF.
func Splice(ctx context.Context, a, b net.Conn) {
	stop := context.AfterFunc(ctx, func() { a.Close(); b.Close() })
	defer stop()
	var wg sync.WaitGroup
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			_ = cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	wg.Add(2)
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}

// ConnectorMCPProxy serves /mcp in the guest and forwards it to the host
// broker's /mcp over dial (vsock port 9001).
func ConnectorMCPProxy(dial Dialer) http.Handler {
	transport := &http.Transport{
		DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "lydiabox-host"
			pr.Out.Host = "lydiabox-host"
			pr.Out.URL.Path = ConnectorMCPPath + strings.TrimPrefix(pr.In.URL.Path, "/mcp")
			pr.Out.URL.RawPath = ""
		},
		Transport:     transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "connector broker unavailable: "+err.Error(), http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" && !strings.HasPrefix(r.URL.Path, "/mcp/") {
			http.NotFound(w, r)
			return
		}
		if !loopbackOrigin(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// loopbackOrigin rejects browser requests from non-local pages, so a web
// page opened in the guest cannot use the bridges.
func loopbackOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		return false
	}
	host := rest
	if h, _, err := net.SplitHostPort(rest); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
