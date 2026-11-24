package storage

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/storage/s3proxy"
)

// S3GatewayPrefix is the public URL path of the virtual S3 gateway on the
// main listener. Clients use the endpoint http://<host>:8080/s3 with
// path-style addressing, so `/s3/lydiabox/<key>` addresses the object <key>
// in the bucket "lydiabox", and `/s3/` lists the buckets.
const S3GatewayPrefix = "/s3"

// S3GatewayBucket is the bucket name under which the virtual S3 gateway
// serves the volume. Object keys are paths from the volume root, so the user
// trees are `workspace/...` and `home/agent/...`.
const S3GatewayBucket = "lydiabox"

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
// (loopback by default) and serves it at /s3 on the main listener.
// S3 clients sign the request path (SigV4), so the proxy (package s3proxy)
// verifies the client signature, removes the /s3 prefix, and signs the
// request again for the backend with the same credentials.
// Clients authenticate with access key "lydiabox" and LYDIABOX_AUTH_TOKEN as
// the secret key. An empty address disables the gateway.
type S3Gateway struct {
	tenant  string
	addr    string
	secret  string
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
	g.secret = token
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

// Handler returns the verify-and-re-sign proxy for S3GatewayPrefix and
// below. It answers 503 while the backend is down or the gateway is
// disabled. The proxy checks the SigV4 signature itself, so the caller must
// not add API authentication.
func (g *S3Gateway) Handler() http.Handler {
	if g.child == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the S3 gateway is disabled (LYDIABOX_S3_GATEWAY_ADDR is empty)", http.StatusServiceUnavailable)
		})
	}
	proxy, err := NewS3Proxy(&url.URL{Scheme: "http", Host: dialAddr(g.addr)}, g.secret)
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.child.Addr() == "" {
			http.Error(w, "the S3 gateway is not running", http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// NewS3Proxy serves the S3 server at backend under S3GatewayPrefix: it
// verifies SigV4 with access key S3GatewayAccessKey and secret, removes the
// prefix, and re-signs the request for the backend with the same
// credentials. The Host header and the query stay unchanged.
func NewS3Proxy(backend *url.URL, secret string) (http.Handler, error) {
	return s3proxy.New(s3proxy.Options{
		AccessKey: S3GatewayAccessKey, SecretKey: secret, Backend: backend, Prefix: S3GatewayPrefix,
	})
}
