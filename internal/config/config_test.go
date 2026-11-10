package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, env map[string]string, yaml string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	opts := LoadOptions{HomeDir: filepath.Join(dir, "home"), Environ: func() []string {
		out := []string{"UNRELATED=1"}
		for k, v := range env {
			out = append(out, k+"="+v)
		}
		return out
	}}
	if yaml != "" {
		p := filepath.Join(dir, "lydiabox.yaml")
		if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		opts.ConfigFile = p
	} else {
		// No explicit file: Load looks for ./lydiabox.yaml, which is absent here.
		t.Chdir(dir)
	}
	return Load(opts)
}

func TestDefaults(t *testing.T) {
	cfg, err := load(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:8080" || cfg.S3.Region != "auto" || cfg.VM.Driver != DriverAuto ||
		cfg.VM.CPUs != 4 || cfg.VM.MemMB != 8192 || cfg.VM.IdleTimeout != 20*time.Minute ||
		cfg.VM.GPUPCIAddr != "" || cfg.OAuthRelayURL != "" || cfg.TenantID != DefaultTenantID ||
		cfg.MetaSyncInterval != time.Minute {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if !strings.HasSuffix(cfg.DataDir, filepath.Join("home", ".lydiabox")) || !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("DataDir = %q", cfg.DataDir)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	err = cfg.ValidateServe()
	for _, want := range []string{"LYDIABOX_S3_ENDPOINT is required", "LYDIABOX_S3_BUCKET is required",
		"LYDIABOX_S3_ACCESS_KEY is required", "LYDIABOX_S3_SECRET_KEY is required"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateServe error %v missing %q", err, want)
		}
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	yaml := `
listen_addr: 127.0.0.1:9000
data_dir: ~/lb
s3:
  endpoint: https://minio.local:9000
  bucket: yaml-bucket
  access_key: yak
  secret_key: yask
vm:
  driver: docker
  cpus: 2
  idle_timeout: 5m
connectors:
  github:
    client_id: yaml-id
`
	env := map[string]string{
		"LYDIABOX_S3_BUCKET":                      "env-bucket",
		"LYDIABOX_VM_MEM_MB":                      "4096",
		"LYDIABOX_IDLE_TIMEOUT":                   "0",
		"LYDIABOX_GITHUB_CLIENT_SECRET":           "env-secret",
		"LYDIABOX_SLACK_CLIENT_ID":                "slack-id",
		"LYDIABOX_MIRROR_CLOUD_HYPERVISOR":        "https://mirror/ch",
		"LYDIABOX_MIRROR_CLOUD_HYPERVISOR_SHA256": strings.Repeat("AB", 32),
	}
	cfg, err := load(t, env, yaml)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" || cfg.S3.Bucket != "env-bucket" || cfg.S3.AccessKey != "yak" ||
		cfg.VM.Driver != DriverDocker || cfg.VM.CPUs != 2 || cfg.VM.MemMB != 4096 || cfg.VM.IdleTimeout != 0 {
		t.Fatalf("precedence wrong: %+v", cfg)
	}
	if !strings.HasSuffix(cfg.DataDir, filepath.Join("home", "lb")) {
		t.Fatalf("~ not expanded: %q", cfg.DataDir)
	}
	if gh := cfg.OAuthClients["github"]; gh.ClientID != "yaml-id" || gh.ClientSecret != "env-secret" {
		t.Fatalf("github client = %+v", gh)
	}
	if cfg.OAuthClients["slack"].ClientID != "slack-id" {
		t.Fatalf("slack client = %+v", cfg.OAuthClients["slack"])
	}
	m := cfg.Mirrors["cloud-hypervisor"]
	if m.URL != "https://mirror/ch" || m.SHA256 != strings.Repeat("ab", 32) {
		t.Fatalf("mirror = %+v", m)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
}

func TestS3GatewayAddr(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		yaml string
		want string
	}{
		{"default", nil, "", DefaultS3GatewayAddr},
		{"yaml", nil, "s3:\n  gateway_addr: 0.0.0.0:9100\n", "0.0.0.0:9100"},
		{"env overrides yaml", map[string]string{"LYDIABOX_S3_GATEWAY_ADDR": "127.0.0.1:9200"}, "s3:\n  gateway_addr: 0.0.0.0:9100\n", "127.0.0.1:9200"},
		{"empty env disables", map[string]string{"LYDIABOX_S3_GATEWAY_ADDR": ""}, "", ""},
		{"off disables", map[string]string{"LYDIABOX_S3_GATEWAY_ADDR": "off"}, "", ""},
		{"empty yaml disables", nil, "s3:\n  gateway_addr: \"\"\n", ""},
	}
	for _, c := range cases {
		cfg, err := load(t, c.env, c.yaml)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if cfg.S3.GatewayAddr != c.want {
			t.Errorf("%s: gateway addr = %q, want %q", c.name, cfg.S3.GatewayAddr, c.want)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := load(t, map[string]string{"LYDIABOX_VM_CPUS": "four"}, ""); err == nil || !strings.Contains(err.Error(), "LYDIABOX_VM_CPUS") {
		t.Fatalf("want integer error, got %v", err)
	}
	if _, err := load(t, map[string]string{"LYDIABOX_IDLE_TIMEOUT": "soon"}, ""); err == nil {
		t.Fatal("want duration error")
	}
	if _, err := load(t, nil, "unknown_key: 1\n"); err == nil || !strings.Contains(err.Error(), "unknown_key") {
		t.Fatalf("want unknown field error, got %v", err)
	}
	if _, err := Load(LoadOptions{ConfigFile: filepath.Join(t.TempDir(), "missing.yaml"), HomeDir: t.TempDir(),
		Environ: func() []string { return nil }}); err == nil {
		t.Fatal("explicit missing config file must fail")
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(c *Config){
		"LYDIABOX_LISTEN_ADDR":        func(c *Config) { c.ListenAddr = "8080" },
		"LYDIABOX_S3_GATEWAY_ADDR":    func(c *Config) { c.S3.GatewayAddr = "9000" },
		"LYDIABOX_AUTH_TOKEN":         func(c *Config) { c.AuthToken = "short" },
		"LYDIABOX_MASTER_KEY":         func(c *Config) { c.MasterKey = "abcd" },
		"LYDIABOX_VM_DRIVER":          func(c *Config) { c.VM.Driver = "qemu" },
		"LYDIABOX_VM_CPUS":            func(c *Config) { c.VM.CPUs = 0 },
		"LYDIABOX_VM_MEM_MB":          func(c *Config) { c.VM.MemMB = 100 },
		"LYDIABOX_GPU_PCI_ADDR":       func(c *Config) { c.VM.GPUPCIAddr = "01:00.0" },
		"LYDIABOX_IDLE_TIMEOUT":       func(c *Config) { c.VM.IdleTimeout = -time.Second },
		"LYDIABOX_META_SYNC_INTERVAL": func(c *Config) { c.MetaSyncInterval = time.Second },
		"LYDIABOX_LOG_LEVEL":          func(c *Config) { c.LogLevel = "loud" },
		"LYDIABOX_OAUTH_RELAY_URL":    func(c *Config) { c.OAuthRelayURL = "ftp://relay" },
		"tenant ID":                   func(c *Config) { c.TenantID = "Bad/Tenant" },
		"LYDIABOX_MIRROR_JUICEFS_SHA256": func(c *Config) {
			c.Mirrors["juicefs"] = Mirror{SHA256: "xyz"}
		},
	}
	for want, mutate := range cases {
		c := Defaults("/home/u")
		mutate(c)
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}
	c := Defaults("/home/u")
	c.VM.GPUPCIAddr = "0000:01:00.0"
	c.S3 = S3Config{Endpoint: "minio:9000", Bucket: "a/b", AccessKey: "k", SecretKey: "s"}
	err := c.ValidateServe()
	if err == nil || !strings.Contains(err.Error(), "LYDIABOX_S3_ENDPOINT") || !strings.Contains(err.Error(), "bare bucket") {
		t.Fatalf("ValidateServe: %v", err)
	}
}

func TestResolveVMDriver(t *testing.T) {
	yes := func() bool { return true }
	no := func() bool { return false }
	for _, tc := range []struct {
		configured string
		kvm        func() bool
		want       string
	}{
		{DriverAuto, yes, DriverCHV},
		{DriverAuto, no, DriverDocker},
		{DriverAuto, nil, DriverDocker},
		{DriverCHV, no, DriverCHV},
		{DriverDocker, yes, DriverDocker},
	} {
		if got := ResolveVMDriver(tc.configured, tc.kvm); got != tc.want {
			t.Errorf("ResolveVMDriver(%q) = %q, want %q", tc.configured, got, tc.want)
		}
	}
	if KVMAccessible(filepath.Join(t.TempDir(), "kvm")) {
		t.Fatal("a missing device must not be accessible")
	}
}

func TestResolvedBucketURL(t *testing.T) {
	for _, tc := range []struct {
		s3   S3Config
		want string
	}{
		{S3Config{Endpoint: "https://s3.us-east-1.amazonaws.com", Bucket: "b"}, "https://b.s3.us-east-1.amazonaws.com"},
		{S3Config{Endpoint: "https://acct.r2.cloudflarestorage.com/", Bucket: "b"}, "https://acct.r2.cloudflarestorage.com/b"},
		{S3Config{Endpoint: "http://127.0.0.1:9000", Bucket: "b"}, "http://127.0.0.1:9000/b"},
		{S3Config{Endpoint: "http://x", Bucket: "b", BucketURL: "https://override/b"}, "https://override/b"},
	} {
		got, err := tc.s3.ResolvedBucketURL()
		if err != nil || got != tc.want {
			t.Errorf("%+v: got %q, %v; want %q", tc.s3, got, err, tc.want)
		}
	}
	if _, err := (S3Config{Endpoint: "::bad", Bucket: "b"}).ResolvedBucketURL(); err == nil {
		t.Fatal("want error for bad endpoint")
	}
}

func TestEnsureSecrets(t *testing.T) {
	c := Defaults(t.TempDir())
	if err := c.EnsureSecrets(); err != nil {
		t.Fatal(err)
	}
	if !c.MasterKeyGenerated || !c.AuthTokenGenerated {
		t.Fatal("secrets must be generated on first run")
	}
	key, err := c.MasterKeyBytes()
	if err != nil || len(key) != MasterKeySize {
		t.Fatalf("master key: %v (%d bytes)", err, len(key))
	}
	for _, p := range []string{c.MasterKeyPath(), c.AuthTokenPath()} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", p, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(c.StateDir()); fi.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %o", fi.Mode().Perm())
	}

	// A second run reads the same secrets back and tightens loose permissions.
	if err := os.Chmod(c.MasterKeyPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := Defaults("")
	c2.DataDir = c.DataDir
	if err := c2.EnsureSecrets(); err != nil {
		t.Fatal(err)
	}
	if c2.MasterKeyGenerated || c2.AuthTokenGenerated || c2.MasterKey != c.MasterKey || c2.AuthToken != c.AuthToken {
		t.Fatal("secrets must persist across runs")
	}
	if fi, _ := os.Stat(c.MasterKeyPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode not tightened: %o", fi.Mode().Perm())
	}

	// Explicit values win and are not written.
	c3 := Defaults(t.TempDir())
	c3.MasterKey = strings.Repeat("0f", 32)
	c3.AuthToken = "explicit-token-0123456789"
	if err := c3.EnsureSecrets(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c3.MasterKeyPath()); !os.IsNotExist(err) {
		t.Fatal("explicit master key must not be written to disk")
	}

	// A corrupt key file is rejected.
	c4 := Defaults(t.TempDir())
	if err := os.MkdirAll(c4.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c4.MasterKeyPath(), []byte("nothex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c4.EnsureSecrets(); err == nil {
		t.Fatal("corrupt master key must fail")
	}
}

func TestLayout(t *testing.T) {
	c := Defaults("/h")
	c.DataDir = "/d"
	want := map[string]string{
		c.DBPath():         "/d/state/lydiabox.db",
		c.BinDir():         "/d/runtime/bin",
		c.ImagesDir():      "/d/runtime/images",
		c.JuiceFSMetaURL(): "sqlite3:///d/meta/jfs.db",
		c.HomeDir():        "/d/mnt/jfs/home/agent",
		c.WorkspaceDir():   "/d/mnt/jfs/workspace",
		c.RootUpperImage(): "/d/mnt/jfs/system/root-upper.img",
		c.CacheDir():       "/d/cache/jfs",
		c.MagpieDir():      "/d/state/magpie",
		c.RunDir():         "/d/run",
	}
	for got, exp := range want {
		if filepath.ToSlash(got) != exp {
			t.Errorf("got %q, want %q", got, exp)
		}
	}
	if MirrorEnvName("cloud-hypervisor") != "LYDIABOX_MIRROR_CLOUD_HYPERVISOR" {
		t.Fatal(MirrorEnvName("cloud-hypervisor"))
	}
}
