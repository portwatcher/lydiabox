package storage

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
)

// S3GatewayPrefix is the public URL path of the virtual S3 gateway on the
// main listener. It is also the bucket name: clients send path-style
// requests to the endpoint http://<host>:8080, so `/s3/<key>` addresses the
// object <key> in the bucket "s3".
const S3GatewayPrefix = "/s3"

// S3GatewayBucket is the bucket name under which the virtual S3 gateway
// serves the volume. Object keys are paths from the volume root, so the user
// trees are `workspace/...` and `home/agent/...`. It equals the public path
// prefix, so the proxy forwards path-style requests unchanged and SigV4
// signatures stay valid.
const S3GatewayBucket = "s3"

// S3GatewayAccessKey is the access key of the virtual S3 gateway; the
// secret key is LYDIABOX_AUTH_TOKEN.
const S3GatewayAccessKey = "lydiabox"

// S3GatewayStatus is reported by GET /api/v1/status under storage.s3Gateway.
type S3GatewayStatus struct {
	Enabled bool `json:"enabled"`
	// Addr is the backend listen address (LYDIABOX_S3_GATEWAY_ADDR).
	Addr string `json:"addr"`
	// Path is the public path on the main listener ("/s3").
	Path    string `json:"path"`
	Bucket  string `json:"bucket"`
	Running bool   `json:"running"`
}

// S3GatewayOptions configures the virtual S3 gateway.
type S3GatewayOptions struct {
	Config        *config.Config
	JuiceFSBinary string
	Runner        Runner
	Logger        *slog.Logger
	ReadyTimeout  time.Duration
	// Dial checks readiness; defaults to a TCP dial.
	Dial func(ctx context.Context, addr string) error
}

// S3Gateway supervises `juicefs gateway` on LYDIABOX_S3_GATEWAY_ADDR
// (loopback by default) and reverse-proxies /s3 on the main listener to it.
// S3 clients sign the request path and the Host header (SigV4), so the proxy
// forwards both unchanged: the bucket is named "s3", and a path-style request
// to http://<host>:8080/s3/<key> verifies at the backend without re-signing.
// Clients authenticate with access key "lydiabox" and LYDIABOX_AUTH_TOKEN as
// the secret key. An empty address disables the gateway.
type S3Gateway struct {
	tenant  string
	addr    string
	logPath string
	child   *childServer
}

// NewS3Gateway validates options. The gateway is disabled when
// Config.S3.GatewayAddr is empty.
func NewS3Gateway(opts S3GatewayOptions) (*S3Gateway, error) {
	if opts.Config == nil || opts.Runner == nil || opts.JuiceFSBinary == "" {
		return nil, errNoBinary
	}
	c := opts.Config
	g := &S3Gateway{tenant: c.TenantID, addr: c.S3.GatewayAddr}
	if g.addr == "" {
		return g, nil
	}
	if len(c.AuthToken) < 8 {
		return nil, fmt.Errorf("storage: the S3 gateway secret key (LYDIABOX_AUTH_TOKEN) needs at least 8 characters")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 60 * time.Second
	}
	if opts.Dial == nil {
		opts.Dial = defaultDial
	}
	jfs := JuiceFS{Binary: opts.JuiceFSBinary, MetaURL: c.JuiceFSMetaURL(), MountDir: c.MountDir()}
	cacheDir := filepath.Join(c.DataDir, "cache", "jfs-gateway")
	logPath := filepath.Join(c.DataDir, "logs", "juicefs-gateway.log")
	g.logPath = logPath
	token := c.AuthToken
	g.child = &childServer{
		name: "juicefs gateway", log: opts.Logger.With("component", "s3gateway", "tenant", c.TenantID),
		runner: opts.Runner, dial: opts.Dial, readyTimeout: opts.ReadyTimeout, fixedAddr: g.addr,
		command: func(addr string) Command {
			return jfs.Gateway(addr, cacheDir, logPath, S3GatewayBucket, S3GatewayAccessKey, token)
		},
	}
	return g, nil
}

// Enabled reports whether a gateway address is configured.
func (g *S3Gateway) Enabled() bool { return g.child != nil }

// Start launches the gateway. It does nothing when the gateway is disabled.
func (g *S3Gateway) Start(ctx context.Context, tenantID string) error {
	if tenantID != g.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	if g.child == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(g.logPath), 0o700); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	if err := g.child.start(ctx); err != nil {
		return fmt.Errorf("%w (set LYDIABOX_S3_GATEWAY_ADDR to another address, or to an empty value to disable the gateway)", err)
	}
	return nil
}

// Stop terminates the gateway.
func (g *S3Gateway) Stop(ctx context.Context, tenantID string) error {
	if tenantID != g.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	if g.child == nil {
		return nil
	}
	return g.child.stop(ctx)
}

// Status reports the gateway state of a tenant.
func (g *S3Gateway) Status(tenantID string) S3GatewayStatus {
	if tenantID != g.tenant || g.child == nil {
		return S3GatewayStatus{}
	}
	return S3GatewayStatus{Enabled: true, Addr: g.addr, Path: S3GatewayPrefix, Bucket: S3GatewayBucket, Running: g.child.Addr() != ""}
}

// Handler returns the reverse proxy for /s3 and /s3/. It answers 503 while
// the backend is down or the gateway is disabled. The backend does its own
// SigV4 authentication, so the caller must not add API authentication.
func (g *S3Gateway) Handler() http.Handler {
	if g.child == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the S3 gateway is disabled (LYDIABOX_S3_GATEWAY_ADDR is empty)", http.StatusServiceUnavailable)
		})
	}
	proxy := NewS3Proxy(&url.URL{Scheme: "http", Host: dialAddr(g.addr)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.child.Addr() == "" {
			http.Error(w, "the S3 gateway is not running", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// NewS3Proxy forwards requests to the S3 server at backend without changes:
// the escaped path, the query, the Host header, and every x-amz-* and
// Authorization header stay as the client signed them. It adds no
// X-Forwarded-* headers.
func NewS3Proxy(backend *url.URL) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = backend.Scheme
			pr.Out.URL.Host = backend.Host
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.URL.RawPath = pr.In.URL.RawPath
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
	}
}
