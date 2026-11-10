package config

import "path/filepath"

// The methods below define the on-disk layout of $LYDIABOX_DATA_DIR. Every
// subsystem resolves its paths through them so the layout stays in one place.
//
//	$DATA_DIR/
//	├── state/            master.key, auth.token, lydiabox.db, magpie/
//	├── runtime/bin/      juicefs, cloud-hypervisor, virtiofsd, magpie
//	├── runtime/images/   vmlinux, initramfs.cpio.gz, rootfs.squashfs
//	├── meta/jfs.db       JuiceFS SQLite metadata engine
//	├── mnt/jfs/          JuiceFS mount point (home/agent, workspace, system)
//	├── cache/jfs/        JuiceFS local block cache
//	├── run/              UNIX sockets and pid files
//	└── tmp/              staging area for snapshots and uploads

// StateDir holds host secrets and the state database.
func (c *Config) StateDir() string { return filepath.Join(c.DataDir, "state") }

// MasterKeyPath is the file that stores the auto-generated master key.
func (c *Config) MasterKeyPath() string { return filepath.Join(c.StateDir(), "master.key") }

// AuthTokenPath is the file that stores the auto-generated admin token.
func (c *Config) AuthTokenPath() string { return filepath.Join(c.StateDir(), "auth.token") }

// DBPath is the host SQLite state database.
func (c *Config) DBPath() string { return filepath.Join(c.StateDir(), "lydiabox.db") }

// MagpieDir is the data directory of the host magpie gateway.
func (c *Config) MagpieDir() string { return filepath.Join(c.StateDir(), "magpie") }

// RuntimeDir holds bootstrapped binaries and guest images.
func (c *Config) RuntimeDir() string { return filepath.Join(c.DataDir, "runtime") }

// BinDir holds the bootstrapped data-plane binaries.
func (c *Config) BinDir() string { return filepath.Join(c.RuntimeDir(), "bin") }

// ImagesDir holds vmlinux, initramfs.cpio.gz, and rootfs.squashfs.
func (c *Config) ImagesDir() string { return filepath.Join(c.RuntimeDir(), "images") }

// MetaDir holds the JuiceFS metadata database.
func (c *Config) MetaDir() string { return filepath.Join(c.DataDir, "meta") }

// JuiceFSMetaPath is the JuiceFS SQLite metadata file.
func (c *Config) JuiceFSMetaPath() string { return filepath.Join(c.MetaDir(), "jfs.db") }

// JuiceFSMetaURL is the JuiceFS metadata engine URL.
func (c *Config) JuiceFSMetaURL() string { return "sqlite3://" + c.JuiceFSMetaPath() }

// MountDir is the JuiceFS mount point.
func (c *Config) MountDir() string { return filepath.Join(c.DataDir, "mnt", "jfs") }

// HomeDir is the guest /home/agent directory on the host.
func (c *Config) HomeDir() string { return filepath.Join(c.MountDir(), "home", "agent") }

// WorkspaceDir is the guest /workspace directory on the host.
func (c *Config) WorkspaceDir() string { return filepath.Join(c.MountDir(), "workspace") }

// SystemDir holds VM system images stored on JuiceFS (root-upper.img).
func (c *Config) SystemDir() string { return filepath.Join(c.MountDir(), "system") }

// RootUpperImage is the persistent OverlayFS upper layer of the guest root.
func (c *Config) RootUpperImage() string { return filepath.Join(c.SystemDir(), "root-upper.img") }

// CacheDir is the JuiceFS local cache directory.
func (c *Config) CacheDir() string { return filepath.Join(c.DataDir, "cache", "jfs") }

// RunDir holds UNIX sockets of supervised processes.
func (c *Config) RunDir() string { return filepath.Join(c.DataDir, "run") }

// TmpDir is a scratch area on the same filesystem as the data directory.
func (c *Config) TmpDir() string { return filepath.Join(c.DataDir, "tmp") }
