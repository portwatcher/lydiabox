package storage

import "strconv"

// JuiceFS command construction, kept separate from process supervision so the
// exact flags of docs/design.md section 4.2 are covered by unit tests.

// CacheSizeMiB is the JuiceFS local cache size passed to `juicefs mount`.
const CacheSizeMiB = 10240

// JuiceFS builds juicefs command lines for one tenant volume.
type JuiceFS struct {
	// Binary is the juicefs executable path.
	Binary string
	// MetaURL is sqlite3://$DATA_DIR/meta/jfs.db.
	MetaURL string
	// MountDir is $DATA_DIR/mnt/jfs.
	MountDir string
	// CacheDir is $DATA_DIR/cache/jfs.
	CacheDir string
}

// VolumeName returns the JuiceFS volume name of a tenant.
func VolumeName(tenantID string) string { return "lydiabox-" + tenantID }

// S3Credentials are the object storage settings passed to `juicefs format`.
type S3Credentials struct {
	BucketURL string
	AccessKey string
	SecretKey string
}

// Format returns `juicefs format` for a new volume.
func (j JuiceFS) Format(tenantID string, s3 S3Credentials) Command {
	return Command{Path: j.Binary, Args: []string{
		"format", j.MetaURL, VolumeName(tenantID),
		"--storage=s3",
		"--bucket=" + s3.BucketURL,
		"--access-key=" + s3.AccessKey,
		"--secret-key=" + s3.SecretKey,
		"--compress=lz4",
	}}
}

// Load returns `juicefs load` reading a metadata dump from stdin.
func (j JuiceFS) Load() Command {
	return Command{Path: j.Binary, Args: []string{"load", j.MetaURL}}
}

// RestoreCredentials returns `juicefs config` that re-applies the bucket
// credentials after a load, because `juicefs dump` omits the secret key.
func (j JuiceFS) RestoreCredentials(s3 S3Credentials) Command {
	return Command{Path: j.Binary, Args: []string{
		"config", j.MetaURL,
		"--bucket=" + s3.BucketURL,
		"--access-key=" + s3.AccessKey,
		"--secret-key=" + s3.SecretKey,
		"--yes",
	}}
}

// Mount returns the foreground `juicefs mount` command from section 4.2.
func (j JuiceFS) Mount() Command {
	return Command{Path: j.Binary, Args: []string{
		"mount", j.MetaURL, j.MountDir,
		"--cache-dir=" + j.CacheDir,
		"--cache-size=" + strconv.Itoa(CacheSizeMiB),
		"--writeback",
	}}
}

// Umount returns `juicefs umount` that waits for writeback data to upload.
func (j JuiceFS) Umount(force bool) Command {
	args := []string{"umount", "--flush"}
	if force {
		args = []string{"umount", "--force"}
	}
	return Command{Path: j.Binary, Args: append(args, j.MountDir)}
}

// Dump returns `juicefs dump` writing JSON metadata to stdout.
func (j JuiceFS) Dump() Command {
	return Command{Path: j.Binary, Args: []string{"dump", j.MetaURL}}
}

// WebDAV returns `juicefs webdav` serving the volume on addr. The backend
// requires the internal Basic Auth credentials passed through the environment.
func (j JuiceFS) WebDAV(addr, cacheDir, user, password string) Command {
	return Command{
		Path: j.Binary,
		Args: []string{"webdav", j.MetaURL, addr, "--cache-dir=" + cacheDir, "--cache-size=1024"},
		Env:  []string{"WEBDAV_USER=" + user, "WEBDAV_PASSWORD=" + password},
	}
}
