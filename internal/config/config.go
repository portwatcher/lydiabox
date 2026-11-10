// Package config loads the LydiaBox host configuration from defaults, an
// optional lydiabox.yaml file, and LYDIABOX_* environment variables (in that
// order of precedence, lowest first), validates it, and provisions the
// auto-generated secrets (admin token and master encryption key).
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultTenantID is the tenant used by the single-user self-hosted mode. All
// internal services take a tenantID parameter so that a multi-tenant control
// plane can reuse them without refactoring.
const DefaultTenantID = "default"

// VM driver identifiers accepted by LYDIABOX_VM_DRIVER.
const (
	DriverAuto   = "auto"
	DriverCHV    = "chv"
	DriverDocker = "docker"
)

// ConnectorProviders lists the connector provider IDs whose OAuth client
// credentials are read from LYDIABOX_<PROVIDER>_CLIENT_ID and
// LYDIABOX_<PROVIDER>_CLIENT_SECRET.
var ConnectorProviders = []string{"github", "gdrive", "notion", "slack"}

// Config is the fully resolved host configuration.
type Config struct {
	// ListenAddr is the address of the web UI, REST API, WebSocket, and WebDAV listener.
	ListenAddr string
	// AuthToken is the admin bearer/cookie secret. EnsureSecrets fills it when empty.
	AuthToken string
	// MasterKey is the 64-character hex form of the 32-byte AES-256-GCM key.
	// EnsureSecrets fills it when empty.
	MasterKey string
	// DataDir is the absolute host data directory (default ~/.lydiabox).
	DataDir string

	S3 S3Config
	VM VMConfig

	// OAuthRelayURL is the optional cloud OAuth broker URL.
	OAuthRelayURL string
	// OAuthClients maps a connector provider ID to its self-hosted OAuth client.
	OAuthClients map[string]OAuthClient
	// RootfsURL is an optional URL of prebuilt guest images.
	RootfsURL string
	// Mirrors maps a runtime binary name (for example "cloud-hypervisor") to a
	// download override taken from LYDIABOX_MIRROR_<NAME>[_SHA256].
	Mirrors map[string]Mirror
	// MetaSyncInterval is the period of the JuiceFS metadata snapshot ticker.
	MetaSyncInterval time.Duration
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// TenantID is the tenant served by this process.
	TenantID string

	// AuthTokenGenerated and MasterKeyGenerated report whether EnsureSecrets
	// created the secret during this run (the CLI prints new tokens once).
	AuthTokenGenerated bool
	MasterKeyGenerated bool
	// Source is the path of the YAML file that was loaded, if any.
	Source string
}

// S3Config holds the object storage settings shared by JuiceFS and the
// metadata backup uploader.
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	// BucketURL overrides the JuiceFS --bucket value derived from Endpoint and Bucket.
	BucketURL string
	// GatewayAddr is the listen address of the virtual S3 gateway
	// (`juicefs gateway`) that serves the volume. Empty disables it.
	GatewayAddr string
}

// DefaultS3GatewayAddr is the default listen address of the virtual S3 gateway.
const DefaultS3GatewayAddr = "127.0.0.1:8090"

// VMConfig holds the guest VM settings consumed by internal/vmm.
type VMConfig struct {
	// Driver is the configured value: auto, chv, or docker.
	Driver      string
	CPUs        int
	MemMB       int
	GPUPCIAddr  string
	IdleTimeout time.Duration
}

// OAuthClient is a self-hosted OAuth application registration.
type OAuthClient struct {
	ClientID     string
	ClientSecret string
}

// Mirror overrides the download source of one runtime binary.
type Mirror struct {
	URL    string
	SHA256 string
}

// Defaults returns the configuration defaults from docs/design.md section 4.1.
// homeDir is used to expand the default data directory.
func Defaults(homeDir string) *Config {
	return &Config{
		ListenAddr:       "0.0.0.0:8080",
		DataDir:          filepath.Join(homeDir, ".lydiabox"),
		S3:               S3Config{Region: "auto", GatewayAddr: DefaultS3GatewayAddr},
		VM:               VMConfig{Driver: DriverAuto, CPUs: 4, MemMB: 8192, IdleTimeout: 20 * time.Minute},
		OAuthClients:     map[string]OAuthClient{},
		Mirrors:          map[string]Mirror{},
		MetaSyncInterval: 60 * time.Second,
		LogLevel:         "info",
		TenantID:         DefaultTenantID,
	}
}

// LoadOptions controls where Load reads its inputs. Zero values select the
// process environment, the user's home directory, and ./lydiabox.yaml.
type LoadOptions struct {
	// ConfigFile is an explicit YAML path. When empty, LYDIABOX_CONFIG is used,
	// then ./lydiabox.yaml if it exists.
	ConfigFile string
	// Environ returns KEY=VALUE pairs. Defaults to os.Environ.
	Environ func() []string
	// HomeDir overrides the home directory used for ~ expansion.
	HomeDir string
}

// Load resolves the configuration. It does not create files; call
// EnsureSecrets afterwards to provision the token and master key.
func Load(opts LoadOptions) (*Config, error) {
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ
	}
	env := envMap(environ())
	home := opts.HomeDir
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("config: resolve home directory: %w", err)
		}
		home = h
	}

	cfg := Defaults(home)
	path := opts.ConfigFile
	explicit := path != ""
	if path == "" {
		path = env["LYDIABOX_CONFIG"]
		explicit = path != ""
	}
	if path == "" {
		path = "lydiabox.yaml"
	}
	if err := applyFile(cfg, path, explicit, home); err != nil {
		return nil, err
	}
	if err := applyEnv(cfg, env, home); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("config: resolve data dir: %w", err)
	}
	cfg.DataDir = abs
	return cfg, nil
}

func envMap(pairs []string) map[string]string {
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// BucketURL returns the value passed to `juicefs format --bucket`. An explicit
// S3.BucketURL wins. AWS endpoints use virtual-hosted style
// (https://<bucket>.s3.<region>.amazonaws.com); other S3-compatible endpoints
// (R2, MinIO, Ceph) use path style (<endpoint>/<bucket>).
func (s S3Config) ResolvedBucketURL() (string, error) {
	if s.BucketURL != "" {
		return s.BucketURL, nil
	}
	u, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("config: invalid S3 endpoint %q", s.Endpoint)
	}
	if strings.HasSuffix(u.Hostname(), ".amazonaws.com") && strings.HasPrefix(u.Hostname(), "s3") {
		return fmt.Sprintf("%s://%s.%s", u.Scheme, s.Bucket, u.Host), nil
	}
	return fmt.Sprintf("%s://%s%s/%s", u.Scheme, u.Host, strings.TrimRight(u.Path, "/"), s.Bucket), nil
}
