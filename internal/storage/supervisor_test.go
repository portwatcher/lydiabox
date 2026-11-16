package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTestSupervisor(t *testing.T) (*Supervisor, *fakeRunner, *memObjects, *fakeState) {
	t.Helper()
	cfg := testConfig(t)
	r := newFakeRunner(cfg)
	objs := newMemObjects()
	st := &fakeState{content: "state-v1"}
	sup, err := NewSupervisor(Options{
		Config: cfg, JuiceFSBinary: "/rt/juicefs", MkfsBinary: "/sbin/mkfs.ext4", Runner: r, Objects: objs,
		State: st, Logger: discardLogger(), RootUpperSize: 1 << 20, RequireRootUpper: true,
		MountTimeout: 5 * time.Second, Chown: func(string, int, int) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return sup, r, objs, st
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}

func TestJuiceFSCommands(t *testing.T) {
	j := JuiceFS{Binary: "/rt/juicefs", MetaURL: "sqlite3:///d/meta/jfs.db", MountDir: "/d/mnt/jfs", CacheDir: "/d/cache/jfs"}
	creds := S3Credentials{BucketURL: "https://acct.r2.cloudflarestorage.com/bkt", AccessKey: "AK", SecretKey: "SK"}
	for _, tc := range []struct {
		cmd  Command
		want []string
	}{
		{j.Format("default", creds), []string{"format", "sqlite3:///d/meta/jfs.db", "lydiabox-default", "--storage=s3",
			"--bucket=https://acct.r2.cloudflarestorage.com/bkt", "--access-key=AK", "--secret-key=SK", "--compress=lz4"}},
		{j.Mount(), []string{"mount", "sqlite3:///d/meta/jfs.db", "/d/mnt/jfs", "--cache-dir=/d/cache/jfs", "--cache-size=10240", "--writeback"}},
		{j.Load(), []string{"load", "sqlite3:///d/meta/jfs.db"}},
		{j.Dump(), []string{"dump", "sqlite3:///d/meta/jfs.db"}},
		{j.Umount(false), []string{"umount", "--flush", "/d/mnt/jfs"}},
		{j.Umount(true), []string{"umount", "--force", "/d/mnt/jfs"}},
		{j.RestoreCredentials(creds), []string{"config", "sqlite3:///d/meta/jfs.db", "--bucket=https://acct.r2.cloudflarestorage.com/bkt",
			"--access-key=AK", "--secret-key=SK", "--yes"}},
		{MkfsExt4("/sbin/mkfs.ext4", "/img"), []string{"-F", "-q", "/img"}},
	} {
		if !reflect.DeepEqual(tc.cmd.Args, tc.want) {
			t.Errorf("args = %q\nwant   %q", tc.cmd.Args, tc.want)
		}
	}
	if s := j.Format("default", creds).String(); strings.Contains(s, "SK") || !strings.Contains(s, "--secret-key=REDACTED") {
		t.Fatalf("secret not redacted: %s", s)
	}
	dav := j.WebDAV("127.0.0.1:4321", "/d/cache/jfs-webdav", "u", "p")
	if !reflect.DeepEqual(dav.Args, []string{"webdav", "sqlite3:///d/meta/jfs.db", "127.0.0.1:4321", "--cache-dir=/d/cache/jfs-webdav", "--cache-size=1024"}) ||
		!slices.Contains(dav.Env, "WEBDAV_USER=u") || !slices.Contains(dav.Env, "WEBDAV_PASSWORD=p") {
		t.Fatalf("webdav = %+v", dav)
	}
	if DefaultRootUpperSize != 40*1024*1024*1024 {
		t.Fatal("root-upper.img must be 40 GiB")
	}
}

func TestFreshStartFormatsMountsAndProvisions(t *testing.T) {
	ctx := context.Background()
	sup, r, objs, _ := newTestSupervisor(t)
	cfg := sup.cfg
	if err := sup.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if got := r.subcommands(); !reflect.DeepEqual(got, []string{"format", "mkfs"}) {
		t.Fatalf("runs = %v", got)
	}
	format, _ := r.findRun("format")
	if !slices.Contains(format.Args, "--bucket=https://acct.r2.cloudflarestorage.com/bkt") || format.Path != "/rt/juicefs" {
		t.Fatalf("format = %+v", format)
	}
	if len(r.starts) != 1 || !reflect.DeepEqual(r.starts[0].Args, sup.jfs.Mount().Args) {
		t.Fatalf("starts = %+v", r.starts)
	}
	for _, d := range []string{cfg.HomeDir(), cfg.WorkspaceDir(), cfg.SystemDir()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Fatalf("%s not provisioned: %v", d, err)
		}
	}
	mkfs, _ := r.findRun("mkfs")
	if !reflect.DeepEqual(mkfs.Args, []string{"-F", "-q", cfg.RootUpperImage() + ".partial"}) {
		t.Fatalf("mkfs = %q", mkfs.Args)
	}
	fi, err := os.Stat(cfg.RootUpperImage())
	if err != nil || fi.Size() != 1<<20 {
		t.Fatalf("root-upper.img: %v size %d", err, fi.Size())
	}
	if st := sup.Status("default"); !st.Mounted || !st.SyncEnabled {
		t.Fatal("status must report mounted with sync")
	}
	if err := sup.Start(ctx, "default"); err == nil {
		t.Fatal("double start must fail")
	}

	if err := sup.Stop(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	subs := r.subcommands()
	if !slices.Contains(subs, "umount") || subs[len(subs)-1] != "dump" {
		t.Fatalf("stop must flush-unmount then sync: %v", subs)
	}
	um, _ := r.findRun("umount")
	if um.Args[1] != "--flush" {
		t.Fatalf("umount = %q", um.Args)
	}
	if got := gunzip(t, objs.objs[MetaBackupKey("default")]); got != r.dump {
		t.Fatalf("uploaded dump = %q", got)
	}
	if got := gunzip(t, objs.objs[StateBackupKey("default")]); got != "state-v1" {
		t.Fatalf("uploaded state = %q", got)
	}
	if sup.Status("default").Mounted {
		t.Fatal("status must report unmounted after stop")
	}

	// A restart with existing metadata neither formats nor recreates the image.
	sup2, r2, _, _ := newTestSupervisor(t)
	sup2.cfg.DataDir = cfg.DataDir
	sup2.jfs = sup.jfs
	r2.cfg, r2.mountDir = cfg, cfg.MountDir()
	if err := sup2.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	defer sup2.Stop(ctx, "default")
	if got := r2.subcommands(); len(got) != 0 {
		t.Fatalf("restart ran %v", got)
	}
}

func TestRestoreFromBackup(t *testing.T) {
	ctx := context.Background()
	sup, r, objs, _ := newTestSupervisor(t)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write([]byte(`{"backup":true}`))
	zw.Close()
	objs.objs[MetaBackupKey("default")] = buf.Bytes()

	if err := sup.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(ctx, "default")
	subs := r.subcommands()
	if !reflect.DeepEqual(subs[:2], []string{"load", "config"}) || slices.Contains(subs, "format") {
		t.Fatalf("runs = %v", subs)
	}
	if string(r.stdin["load"]) != `{"backup":true}` {
		t.Fatalf("load stdin = %q", r.stdin["load"])
	}
	cfgCmd, _ := r.findRun("config")
	if !slices.Contains(cfgCmd.Args, "--secret-key=SECRET") || !slices.Contains(cfgCmd.Args, "--access-key=AKID") {
		t.Fatalf("config = %q", cfgCmd.Args)
	}
}

func TestFailedFormatRemovesPartialMetadata(t *testing.T) {
	sup, r, _, _ := newTestSupervisor(t)
	r.failRun["format"] = errors.New("bucket access denied")
	err := sup.Start(context.Background(), "default")
	if err == nil || !strings.Contains(err.Error(), "bucket access denied") {
		t.Fatalf("want format error, got %v", err)
	}
	if _, err := os.Stat(sup.cfg.JuiceFSMetaPath()); !os.IsNotExist(err) {
		t.Fatal("partial metadata must be removed so the next start retries")
	}
}

func TestSyncUploadsOnlyChanges(t *testing.T) {
	ctx := context.Background()
	sup, r, objs, st := newTestSupervisor(t)
	if err := sup.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(ctx, "default")
	meta, state := MetaBackupKey("default"), StateBackupKey("default")
	if meta != ".lydiabox-system/tenants/default/jfs-meta.json.gz" {
		t.Fatalf("meta key = %s", meta)
	}

	if err := sup.SyncNow(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	first := sup.Status("default")
	if objs.putCount(meta) != 1 || objs.putCount(state) != 1 || first.LastSyncAt.IsZero() || first.LastUploadAt.IsZero() {
		t.Fatalf("first sync: puts %d/%d status %+v", objs.putCount(meta), objs.putCount(state), first)
	}
	if err := sup.SyncNow(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if objs.putCount(meta) != 1 || objs.putCount(state) != 1 {
		t.Fatal("unchanged snapshots must not be uploaded again")
	}
	if !sup.Status("default").LastUploadAt.Equal(first.LastUploadAt) {
		t.Fatal("LastUploadAt must not move without an upload")
	}

	r.mu.Lock()
	r.dump = `{"changed":1}`
	r.mu.Unlock()
	st.mu.Lock()
	st.content = "state-v2"
	st.mu.Unlock()
	if err := sup.SyncNow(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if objs.putCount(meta) != 2 || objs.putCount(state) != 2 {
		t.Fatal("changed snapshots must be uploaded")
	}

	r.failRun["dump"] = errors.New("dump failed")
	if err := sup.SyncNow(ctx, "default"); err == nil {
		t.Fatal("want sync error")
	}
	if !strings.Contains(sup.Status("default").LastSyncError, "dump failed") {
		t.Fatalf("status = %+v", sup.Status("default"))
	}
	delete(r.failRun, "dump")
	if err := sup.SyncNow(ctx, "other"); !errors.Is(err, ErrUnknownTenant) {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestMountRestartsAfterCrash(t *testing.T) {
	ctx := context.Background()
	sup, r, _, _ := newTestSupervisor(t)
	if err := sup.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	defer sup.Stop(ctx, "default")
	r.mu.Lock()
	first := r.procs[0]
	r.mu.Unlock()
	first.finish(errors.New("fuse: transport endpoint is not connected"))

	deadline := time.Now().Add(10 * time.Second)
	for {
		r.mu.Lock()
		n := len(r.starts)
		r.mu.Unlock()
		if n == 2 && sup.Status("default").Mounted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mount was not restarted (starts=%d)", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	um, ok := r.findRun("umount")
	if !ok || um.Args[1] != "--force" {
		t.Fatalf("a stale mount must be force-unmounted before restart: %+v", um)
	}
}

func TestTenantScoping(t *testing.T) {
	sup, _, _, _ := newTestSupervisor(t)
	if err := sup.Start(context.Background(), "acme"); !errors.Is(err, ErrUnknownTenant) {
		t.Fatalf("Start: %v", err)
	}
	if _, err := sup.HomeDir("acme"); !errors.Is(err, ErrUnknownTenant) {
		t.Fatalf("HomeDir: %v", err)
	}
	if d, err := sup.WorkspaceDir("default"); err != nil || d != sup.cfg.WorkspaceDir() {
		t.Fatalf("WorkspaceDir = %q, %v", d, err)
	}
	if st := sup.Status("acme"); st.Mounted || st.TenantID != "acme" {
		t.Fatalf("Status = %+v", st)
	}
	if err := sup.Stop(context.Background(), "default"); err != nil {
		t.Fatal("stopping a never-started supervisor must be a no-op")
	}
}

func TestOptionalRootUpper(t *testing.T) {
	sup, r, _, _ := newTestSupervisor(t)
	sup.opts.RequireRootUpper = false
	r.failRun["mkfs"] = errors.New("mkfs.ext4: not found")
	if err := sup.Start(context.Background(), "default"); err != nil {
		t.Fatalf("docker driver must tolerate a missing mkfs.ext4: %v", err)
	}
	defer sup.Stop(context.Background(), "default")
	if _, err := os.Stat(sup.cfg.RootUpperImage()); !os.IsNotExist(err) {
		t.Fatal("no image must remain after a failed mkfs")
	}
	if _, err := os.Stat(sup.cfg.RootUpperImage() + ".partial"); !os.IsNotExist(err) {
		t.Fatal("partial image must be removed")
	}
}
