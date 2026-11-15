package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
)

var (
	gpuAddrPattern = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)
	tenantPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	logLevels      = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
)

// MinAuthTokenLength is the shortest accepted admin token.
const MinAuthTokenLength = 16

// Validate checks settings that every command depends on.
func (c *Config) Validate() error {
	var errs []error
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		errs = append(errs, fmt.Errorf("LYDIABOX_LISTEN_ADDR %q: %w", c.ListenAddr, err))
	}
	if c.S3.GatewayAddr != "" {
		if _, port, err := net.SplitHostPort(c.S3.GatewayAddr); err != nil || port == "" || port == "0" {
			errs = append(errs, fmt.Errorf("LYDIABOX_S3_GATEWAY_ADDR %q must be host:port (empty or \"off\" disables the gateway)", c.S3.GatewayAddr))
		}
	}
	if c.AuthToken != "" && len(c.AuthToken) < MinAuthTokenLength {
		errs = append(errs, fmt.Errorf("LYDIABOX_AUTH_TOKEN must have at least %d characters", MinAuthTokenLength))
	}
	if c.MasterKey != "" {
		if _, err := DecodeMasterKey(c.MasterKey); err != nil {
			errs = append(errs, err)
		}
	}
	if !tenantPattern.MatchString(c.TenantID) {
		errs = append(errs, fmt.Errorf("tenant ID %q must match %s", c.TenantID, tenantPattern))
	}
	switch c.VM.Driver {
	case DriverAuto, DriverCHV, DriverDocker:
	default:
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_DRIVER %q must be auto, chv, or docker", c.VM.Driver))
	}
	if c.VM.CPUs < 1 || c.VM.CPUs > 256 {
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_CPUS %d must be between 1 and 256", c.VM.CPUs))
	}
	if c.VM.MemMB < 512 {
		errs = append(errs, fmt.Errorf("LYDIABOX_VM_MEM_MB %d must be at least 512", c.VM.MemMB))
	}
	if c.VM.GPUPCIAddr != "" && !gpuAddrPattern.MatchString(c.VM.GPUPCIAddr) {
		errs = append(errs, fmt.Errorf("LYDIABOX_GPU_PCI_ADDR %q must look like 0000:01:00.0", c.VM.GPUPCIAddr))
	}
	if c.VM.IdleTimeout < 0 {
		errs = append(errs, errors.New("LYDIABOX_IDLE_TIMEOUT must not be negative"))
	}
	if c.MetaSyncInterval < 5e9 {
		errs = append(errs, errors.New("LYDIABOX_META_SYNC_INTERVAL must be at least 5s"))
	}
	if !logLevels[strings.ToLower(c.LogLevel)] {
		errs = append(errs, fmt.Errorf("LYDIABOX_LOG_LEVEL %q must be debug, info, warn, or error", c.LogLevel))
	}
	for _, u := range []struct{ name, val string }{
		{"LYDIABOX_OAUTH_RELAY_URL", c.OAuthRelayURL},
		{"LYDIABOX_ROOTFS_URL", c.RootfsURL},
	} {
		if u.val != "" {
			if err := checkHTTPURL(u.val); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", u.name, err))
			}
		}
	}
	errs = append(errs, c.WebRTC.validate()...)
	errs = append(errs, c.Remote.validate()...)
	errs = append(errs, c.validateCell()...)
	for name, m := range c.Mirrors {
		if m.SHA256 != "" {
			if b, err := hex.DecodeString(m.SHA256); err != nil || len(b) != 32 {
				errs = append(errs, fmt.Errorf("%s_SHA256 must be 64 hex characters", MirrorEnvName(name)))
			}
		}
	}
	return joinErrs(errs)
}

// ValidateServe checks the settings required by `lydiabox up`, which needs the
// S3 bucket that backs the persistent filesystem.
func (c *Config) ValidateServe() error {
	errs := []error{}
	if err := c.Validate(); err != nil {
		errs = append(errs, err)
	}
	required := []struct{ name, val string }{
		{"LYDIABOX_S3_ENDPOINT", c.S3.Endpoint},
		{"LYDIABOX_S3_BUCKET", c.S3.Bucket},
		{"LYDIABOX_S3_ACCESS_KEY", c.S3.AccessKey},
		{"LYDIABOX_S3_SECRET_KEY", c.S3.SecretKey},
	}
	for _, r := range required {
		if r.val == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.name))
		}
	}
	if c.S3.Endpoint != "" {
		if err := checkHTTPURL(c.S3.Endpoint); err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_S3_ENDPOINT: %w", err))
		}
	}
	if c.S3.BucketURL != "" {
		if err := checkHTTPURL(c.S3.BucketURL); err != nil {
			errs = append(errs, fmt.Errorf("LYDIABOX_S3_BUCKET_URL: %w", err))
		}
	}
	if c.S3.Bucket != "" && strings.ContainsAny(c.S3.Bucket, "/ ") {
		errs = append(errs, fmt.Errorf("LYDIABOX_S3_BUCKET %q must be a bare bucket name", c.S3.Bucket))
	}
	return joinErrs(errs)
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an http(s) URL", raw)
	}
	return nil
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
}

// KVMDevice is the device probed by driver auto-detection.
const KVMDevice = "/dev/kvm"

// KVMAccessible reports whether the KVM device at path can be opened for
// read and write by the current user on a Linux host.
func KVMAccessible(path string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// ResolveVMDriver maps the configured driver to a concrete backend. "auto"
// selects chv when kvmAvailable reports true, else docker.
func ResolveVMDriver(configured string, kvmAvailable func() bool) string {
	if configured != DriverAuto {
		return configured
	}
	if kvmAvailable != nil && kvmAvailable() {
		return DriverCHV
	}
	return DriverDocker
}

// EffectiveVMDriver resolves the configured driver against /dev/kvm.
func (c *Config) EffectiveVMDriver() string {
	return ResolveVMDriver(c.VM.Driver, func() bool { return KVMAccessible(KVMDevice) })
}
