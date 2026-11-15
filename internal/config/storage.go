package config

import (
	"fmt"
	"strings"
)

// Storage modes accepted by LYDIABOX_STORAGE_MODE.
const (
	// StorageJuiceFS keeps /home/agent and /workspace on a host JuiceFS
	// volume backed by the S3 bucket (default).
	StorageJuiceFS = "juicefs"
	// StorageProvider keeps them on the disk of the agent machine (for
	// example an InstaBox machine at /data). The host reaches the files
	// through the guest daemon; JuiceFS, WebDAV, and the /s3 gateway are
	// not used, and the S3 settings are optional (they enable the state
	// database backup only).
	StorageProvider = "provider"
)

// StorageConfig selects where the persistent guest trees live.
type StorageConfig struct {
	Mode string
}

type storageFile struct {
	Mode *string `yaml:"mode"`
}

func applyStorageFile(cfg *Config, fc *storageFile) {
	if fc.Mode != nil {
		cfg.Storage.Mode = strings.ToLower(strings.TrimSpace(*fc.Mode))
	}
}

func applyStorageEnv(cfg *Config, env map[string]string) {
	if v := strings.TrimSpace(env["LYDIABOX_STORAGE_MODE"]); v != "" {
		cfg.Storage.Mode = strings.ToLower(v)
	}
}

func (s StorageConfig) validate() []error {
	switch s.Mode {
	case StorageJuiceFS, StorageProvider:
		return nil
	}
	return []error{fmt.Errorf("LYDIABOX_STORAGE_MODE %q must be juicefs or provider", s.Mode)}
}

// ProviderStorage reports whether the guest trees live on the machine disk.
func (c *Config) ProviderStorage() bool { return c.Storage.Mode == StorageProvider }

// S3Configured reports whether every S3 setting needed for uploads is set.
func (c *Config) S3Configured() bool {
	return c.S3.Endpoint != "" && c.S3.Bucket != "" && c.S3.AccessKey != "" && c.S3.SecretKey != ""
}

// S3Partial reports whether some, but not all, S3 settings are set.
func (c *Config) S3Partial() bool {
	set := c.S3.Endpoint != "" || c.S3.Bucket != "" || c.S3.AccessKey != "" || c.S3.SecretKey != ""
	return set && !c.S3Configured()
}
