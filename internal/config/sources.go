package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// fileConfig mirrors lydiabox.yaml. Pointer fields distinguish "absent" from
// "set to the zero value".
type fileConfig struct {
	ListenAddr       *string `yaml:"listen_addr"`
	AuthToken        *string `yaml:"auth_token"`
	MasterKey        *string `yaml:"master_key"`
	DataDir          *string `yaml:"data_dir"`
	OAuthRelayURL    *string `yaml:"oauth_relay_url"`
	RootfsURL        *string `yaml:"rootfs_url"`
	MetaSyncInterval *string `yaml:"meta_sync_interval"`
	LogLevel         *string `yaml:"log_level"`
	S3               struct {
		Endpoint  *string `yaml:"endpoint"`
		Region    *string `yaml:"region"`
		Bucket    *string `yaml:"bucket"`
		AccessKey *string `yaml:"access_key"`
		SecretKey *string `yaml:"secret_key"`
		BucketURL *string `yaml:"bucket_url"`
		// GatewayAddr is the virtual S3 gateway address; "" disables it.
		GatewayAddr *string `yaml:"gateway_addr"`
	} `yaml:"s3"`
	VM struct {
		Driver      *string `yaml:"driver"`
		CPUs        *int    `yaml:"cpus"`
		MemMB       *int    `yaml:"mem_mb"`
		GPUPCIAddr  *string `yaml:"gpu_pci_addr"`
		IdleTimeout *string `yaml:"idle_timeout"`
		vmCellFile  `yaml:",inline"`
	} `yaml:"vm"`
	Connectors map[string]struct {
		ClientID     string `yaml:"client_id"`
		ClientSecret string `yaml:"client_secret"`
	} `yaml:"connectors"`
	// WebRTC is the desktop stream ICE section (webrtc.go).
	WebRTC webrtcFile `yaml:"webrtc"`
	// RemoteAccess is the Cloudflare Zero Trust section (remote.go).
	RemoteAccess remoteFile `yaml:"remote_access"`
	// Cell holds the cell-mode keys (cell.go).
	Cell cellFile `yaml:",inline"`
	// Storage is the storage mode section (storage.go).
	Storage storageFile `yaml:"storage"`
}

func applyFile(cfg *Config, path string, explicit bool, home string) error {
	data, err := os.ReadFile(expandHome(path, home))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return nil
		}
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.Source = path

	setStr(&cfg.ListenAddr, fc.ListenAddr)
	setStr(&cfg.AuthToken, fc.AuthToken)
	setStr(&cfg.MasterKey, fc.MasterKey)
	if fc.DataDir != nil {
		cfg.DataDir = expandHome(*fc.DataDir, home)
	}
	setStr(&cfg.OAuthRelayURL, fc.OAuthRelayURL)
	setStr(&cfg.RootfsURL, fc.RootfsURL)
	setStr(&cfg.LogLevel, fc.LogLevel)
	setStr(&cfg.S3.Endpoint, fc.S3.Endpoint)
	setStr(&cfg.S3.Region, fc.S3.Region)
	setStr(&cfg.S3.Bucket, fc.S3.Bucket)
	setStr(&cfg.S3.AccessKey, fc.S3.AccessKey)
	setStr(&cfg.S3.SecretKey, fc.S3.SecretKey)
	setStr(&cfg.S3.BucketURL, fc.S3.BucketURL)
	if fc.S3.GatewayAddr != nil {
		cfg.S3.GatewayAddr = gatewayAddr(*fc.S3.GatewayAddr)
	}
	setStr(&cfg.VM.Driver, fc.VM.Driver)
	setStr(&cfg.VM.GPUPCIAddr, fc.VM.GPUPCIAddr)
	if fc.VM.CPUs != nil {
		cfg.VM.CPUs = *fc.VM.CPUs
	}
	if fc.VM.MemMB != nil {
		cfg.VM.MemMB = *fc.VM.MemMB
	}
	if err := setDuration(&cfg.VM.IdleTimeout, fc.VM.IdleTimeout, "vm.idle_timeout"); err != nil {
		return err
	}
	if err := setDuration(&cfg.MetaSyncInterval, fc.MetaSyncInterval, "meta_sync_interval"); err != nil {
		return err
	}
	for provider, c := range fc.Connectors {
		cfg.OAuthClients[strings.ToLower(provider)] = OAuthClient{ClientID: c.ClientID, ClientSecret: c.ClientSecret}
	}
	applyRemoteFile(cfg, &fc)
	applyCellFile(cfg, &fc)
	applyStorageFile(cfg, &fc.Storage)
	return applyWebRTCFile(&cfg.WebRTC, &fc.WebRTC)
}

// gatewayAddr normalizes the S3 gateway address: "" and "off" disable it.
func gatewayAddr(v string) string {
	v = strings.TrimSpace(v)
	if strings.EqualFold(v, "off") {
		return ""
	}
	return v
}

func setStr(dst *string, v *string) {
	if v != nil {
		*dst = strings.TrimSpace(*v)
	}
}

func setDuration(dst *time.Duration, v *string, name string) error {
	if v == nil {
		return nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(*v))
	if err != nil {
		return fmt.Errorf("config: %s: %w", name, err)
	}
	*dst = d
	return nil
}

func applyEnv(cfg *Config, env map[string]string, home string) error {
	str := func(key string, dst *string) {
		if v, ok := env[key]; ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	str("LYDIABOX_LISTEN_ADDR", &cfg.ListenAddr)
	str("LYDIABOX_AUTH_TOKEN", &cfg.AuthToken)
	str("LYDIABOX_MASTER_KEY", &cfg.MasterKey)
	if v := strings.TrimSpace(env["LYDIABOX_DATA_DIR"]); v != "" {
		cfg.DataDir = expandHome(v, home)
	}
	str("LYDIABOX_S3_ENDPOINT", &cfg.S3.Endpoint)
	str("LYDIABOX_S3_REGION", &cfg.S3.Region)
	str("LYDIABOX_S3_BUCKET", &cfg.S3.Bucket)
	str("LYDIABOX_S3_ACCESS_KEY", &cfg.S3.AccessKey)
	str("LYDIABOX_S3_SECRET_KEY", &cfg.S3.SecretKey)
	str("LYDIABOX_S3_BUCKET_URL", &cfg.S3.BucketURL)
	// Unlike the other variables, an empty value is meaningful here: it
	// disables the gateway.
	if v, ok := env["LYDIABOX_S3_GATEWAY_ADDR"]; ok {
		cfg.S3.GatewayAddr = gatewayAddr(v)
	}
	str("LYDIABOX_VM_DRIVER", &cfg.VM.Driver)
	str("LYDIABOX_GPU_PCI_ADDR", &cfg.VM.GPUPCIAddr)
	str("LYDIABOX_OAUTH_RELAY_URL", &cfg.OAuthRelayURL)
	str("LYDIABOX_ROOTFS_URL", &cfg.RootfsURL)
	str("LYDIABOX_LOG_LEVEL", &cfg.LogLevel)

	var errs []error
	intVar := func(key string, dst *int) {
		if v := strings.TrimSpace(env[key]); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %q is not an integer", key, v))
				return
			}
			*dst = n
		}
	}
	durVar := func(key string, dst *time.Duration) {
		if v := strings.TrimSpace(env[key]); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %q is not a duration", key, v))
				return
			}
			*dst = d
		}
	}
	intVar("LYDIABOX_VM_CPUS", &cfg.VM.CPUs)
	intVar("LYDIABOX_VM_MEM_MB", &cfg.VM.MemMB)
	durVar("LYDIABOX_IDLE_TIMEOUT", &cfg.VM.IdleTimeout)
	durVar("LYDIABOX_META_SYNC_INTERVAL", &cfg.MetaSyncInterval)

	for _, p := range ConnectorProviders {
		prefix := "LYDIABOX_" + strings.ToUpper(p) + "_"
		c := cfg.OAuthClients[p]
		str(prefix+"CLIENT_ID", &c.ClientID)
		str(prefix+"CLIENT_SECRET", &c.ClientSecret)
		if c.ClientID != "" || c.ClientSecret != "" {
			cfg.OAuthClients[p] = c
		}
	}
	applyMirrors(cfg, env)
	errs = append(errs, applyWebRTCEnv(&cfg.WebRTC, env)...)
	applyRemoteEnv(cfg, env)
	errs = append(errs, applyCellEnv(cfg, env)...)
	applyStorageEnv(cfg, env)
	if len(errs) > 0 {
		return fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return nil
}

// applyMirrors collects LYDIABOX_MIRROR_<NAME>=url and
// LYDIABOX_MIRROR_<NAME>_SHA256=hex. NAME is the binary name upper-cased with
// dashes replaced by underscores (CLOUD_HYPERVISOR -> cloud-hypervisor).
func applyMirrors(cfg *Config, env map[string]string) {
	const prefix = "LYDIABOX_MIRROR_"
	keys := make([]string, 0)
	for k := range env {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(env[k])
		if v == "" {
			continue
		}
		raw := strings.TrimPrefix(k, prefix)
		isSum := strings.HasSuffix(raw, "_SHA256")
		raw = strings.TrimSuffix(raw, "_SHA256")
		name := strings.ToLower(strings.ReplaceAll(raw, "_", "-"))
		if name == "" {
			continue
		}
		m := cfg.Mirrors[name]
		if isSum {
			m.SHA256 = strings.ToLower(v)
		} else {
			m.URL = v
		}
		cfg.Mirrors[name] = m
	}
}

// MirrorEnvName returns the LYDIABOX_MIRROR_* variable name of a binary.
func MirrorEnvName(binary string) string {
	return "LYDIABOX_MIRROR_" + strings.ToUpper(strings.ReplaceAll(binary, "-", "_"))
}
