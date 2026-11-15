package config

import (
	"strings"
	"testing"
)

func TestStorageMode(t *testing.T) {
	cfg, err := load(t, nil, "")
	if err != nil || cfg.Storage.Mode != StorageJuiceFS || cfg.ProviderStorage() {
		t.Fatalf("default mode = %+v, %v", cfg.Storage, err)
	}
	cfg, err = load(t, map[string]string{"LYDIABOX_STORAGE_MODE": "Provider"}, "storage:\n  mode: juicefs\n")
	if err != nil || !cfg.ProviderStorage() {
		t.Fatalf("env must win over YAML: %+v, %v", cfg.Storage, err)
	}
	// No S3 settings are needed in the provider mode.
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("provider mode without S3: %v", err)
	}
	if got := cfg.EffectiveVMDriver(); got != DriverDocker {
		t.Fatalf("auto driver in the provider mode = %q", got)
	}
	// A partial S3 configuration is an error.
	cfg.S3.Endpoint = "https://s3.example.com"
	if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), "LYDIABOX_S3_BUCKET is required when other S3 settings are set") {
		t.Fatalf("partial S3 = %v", err)
	}
	cfg.S3.Bucket, cfg.S3.AccessKey, cfg.S3.SecretKey = "b", "k", "s"
	if err := cfg.ValidateServe(); err != nil || !cfg.S3Configured() {
		t.Fatalf("complete S3 in the provider mode: %v", err)
	}
	cfg.VM.Driver = DriverCHV
	if err := cfg.ValidateServe(); err == nil || !strings.Contains(err.Error(), "LYDIABOX_VM_DRIVER=chv needs LYDIABOX_STORAGE_MODE=juicefs") {
		t.Fatalf("chv with the provider mode = %v", err)
	}
	cfg, err = load(t, nil, "storage:\n  mode: provider\n")
	if err != nil || !cfg.ProviderStorage() {
		t.Fatalf("YAML mode = %+v, %v", cfg.Storage, err)
	}
	cfg, err = load(t, map[string]string{"LYDIABOX_STORAGE_MODE": "nfs"}, "")
	if err == nil {
		err = cfg.Validate()
	}
	if err == nil || !strings.Contains(err.Error(), `LYDIABOX_STORAGE_MODE "nfs" must be juicefs or provider`) {
		t.Fatalf("bad mode = %v", err)
	}
}
