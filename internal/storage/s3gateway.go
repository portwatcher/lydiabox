package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
)

// S3GatewayBucket is the bucket name under which the virtual S3 gateway
// serves the volume. Object keys are paths from the volume root, so the user
// trees are `workspace/...` and `home/agent/...`.
const S3GatewayBucket = "lydiabox"

// S3GatewayAccessKey is the access key of the virtual S3 gateway; the
// secret key is LYDIABOX_AUTH_TOKEN.
const S3GatewayAccessKey = "lydiabox"

// S3GatewayStatus is reported by GET /api/v1/status under storage.s3Gateway.
type S3GatewayStatus struct {
	Enabled bool   `json:"enabled"`
	Addr    string `json:"addr"`
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

// S3Gateway supervises `juicefs gateway` on LYDIABOX_S3_GATEWAY_ADDR. It
// listens on its own address because S3 clients sign the request path
// (SigV4), which a reverse proxy under a path prefix would change. Clients
// authenticate with access key "lydiabox" and LYDIABOX_AUTH_TOKEN as the
// secret key. An empty address disables the gateway.
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
	return S3GatewayStatus{Enabled: true, Addr: g.addr, Bucket: S3GatewayBucket, Running: g.child.Addr() != ""}
}
