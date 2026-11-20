//go:build unix

package remotefs_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/agentfs/agentfstest"
	"github.com/lydiabox/lydiabox/internal/agentfs/remotefs"
)

const tid = "default"

func start(t *testing.T) (*agentfstest.Guest, string) {
	t.Helper()
	base := t.TempDir()
	secret := filepath.Join(base, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "passwd"), []byte("root:x:0:0"), 0o600); err != nil {
		t.Fatal(err)
	}
	return agentfstest.Start(t, filepath.Join(base, "data", "home"), filepath.Join(base, "data", "workspace")), secret
}

// TestLargeStreams uploads and downloads a file larger than many RPC frames
// and windows, and reads ranges through Seek.
func TestLargeStreams(t *testing.T) {
	g, _ := start(t)
	ctx := context.Background()
	data := make([]byte, 24<<20+12345)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	res, err := g.FS.Write(ctx, tid, agentfs.RootWorkspace, "big/blob.bin", bytes.NewReader(data),
		agentfs.WriteOptions{MkdirParents: true, Limit: -1})
	if err != nil || res.Size != int64(len(data)) || res.Replaced {
		t.Fatalf("write = %+v, %v", res, err)
	}
	if g.Waker.Leases(remotefs.LeaseFiles) != 0 || g.Waker.Wakes() == 0 {
		t.Fatalf("leases %d wakes %d", g.Waker.Leases(remotefs.LeaseFiles), g.Waker.Wakes())
	}
	onDisk, err := os.ReadFile(filepath.Join(g.Workspace, "big", "blob.bin"))
	if err != nil || sha256.Sum256(onDisk) != sha256.Sum256(data) {
		t.Fatalf("guest content differs (%v)", err)
	}

	f, err := g.FS.Open(ctx, tid, agentfs.RootWorkspace, "big/blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	if g.Waker.Leases(remotefs.LeaseFiles) != 1 {
		t.Fatal("an open file must hold the files lease")
	}
	got, err := io.ReadAll(f)
	if err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("download differs: %d bytes, %v", len(got), err)
	}
	for _, off := range []int64{0, 1, 4<<20 - 1, int64(len(data)) - 10} {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 10)
		if _, err := io.ReadFull(f, buf); err != nil || !bytes.Equal(buf, data[off:off+10]) {
			t.Fatalf("range at %d: %v", off, err)
		}
	}
	if end, _ := f.Seek(0, io.SeekEnd); end != int64(len(data)) {
		t.Fatalf("seek end = %d", end)
	}
	if n, err := f.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read at end = %d, %v", n, err)
	}
	f.Close()
	if g.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatal("Close must release the files lease")
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("client went away")
	}
	n := min(len(p), r.n)
	r.n -= n
	return n, nil
}

func TestWriteAbortAndLimits(t *testing.T) {
	g, _ := start(t)
	ctx := context.Background()
	_, err := g.FS.Write(ctx, tid, agentfs.RootHome, "partial.bin", &failingReader{n: 3 << 20}, agentfs.WriteOptions{Limit: -1})
	if err == nil || !strings.Contains(err.Error(), "client went away") {
		t.Fatalf("aborted write = %v", err)
	}
	_, err = g.FS.Write(ctx, tid, agentfs.RootHome, "big.bin", bytes.NewReader(make([]byte, 5000)), agentfs.WriteOptions{Limit: 4096})
	if !errors.Is(err, agentfs.ErrTooLarge) {
		t.Fatalf("over the limit = %v", err)
	}
	if _, err := g.FS.Write(ctx, tid, agentfs.RootHome, "a.txt", strings.NewReader("1"), agentfs.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.FS.Write(ctx, tid, agentfs.RootHome, "a.txt", strings.NewReader("2"), agentfs.WriteOptions{}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("write without overwrite = %v", err)
	}
	waitNoTemp(t, g.Home)
}

// waitNoTemp waits until the guest removed its temporary files (an aborted
// write ends asynchronously in the guest).
func waitNoTemp(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(dir)
		var tmp []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") || e.Name() == "partial.bin" || e.Name() == "big.bin" {
				tmp = append(tmp, e.Name())
			}
		}
		if len(tmp) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("files left behind: %v", tmp)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTreeOperationsAndEscapes(t *testing.T) {
	g, secret := start(t)
	ctx := context.Background()
	// Config files: create with parents, keep the mode of an existing file.
	if err := g.FS.WriteFile(ctx, tid, agentfs.RootHome, ".codex/config.toml", []byte("a = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(g.Home, ".codex", "config.toml"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := g.FS.WriteFile(ctx, tid, agentfs.RootHome, ".codex/config.toml", []byte("a = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(g.Home, ".codex", "config.toml"))
	if b, err := g.FS.ReadFile(ctx, tid, agentfs.RootHome, ".codex/config.toml"); err != nil || string(b) != "a = 2\n" || fi.Mode().Perm() != 0o640 {
		t.Fatalf("read back %q %v mode %v", b, err, fi.Mode())
	}
	// A symlink inside the root is written through; one that leaves it is refused.
	os.MkdirAll(filepath.Join(g.Home, "dotfiles"), 0o755)
	os.WriteFile(filepath.Join(g.Home, "dotfiles", "settings.json"), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(g.Home, ".claude"), 0o755)
	os.Symlink("../dotfiles/settings.json", filepath.Join(g.Home, ".claude", "settings.json"))
	if err := g.FS.WriteFile(ctx, tid, agentfs.RootHome, ".claude/settings.json", []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(g.Home, "dotfiles", "settings.json")); string(b) != `{"x":1}` {
		t.Fatalf("write through symlink: %q", b)
	}
	if fi, _ := os.Lstat(filepath.Join(g.Home, ".claude", "settings.json")); fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
	os.Symlink(filepath.Join(secret, "passwd"), filepath.Join(g.Home, "abs-link"))
	os.Symlink("../../secret/passwd", filepath.Join(g.Home, "rel-link"))
	os.Symlink(secret, filepath.Join(g.Home, "dir-link"))
	for _, p := range []string{"abs-link", "rel-link", "dir-link/passwd"} {
		if _, err := g.FS.ReadFile(ctx, tid, agentfs.RootHome, p); !errors.Is(err, agentfs.ErrEscapes) {
			t.Errorf("ReadFile(%q) = %v", p, err)
		}
		if err := g.FS.WriteFile(ctx, tid, agentfs.RootHome, p, []byte("pwned"), 0o644); !errors.Is(err, agentfs.ErrEscapes) {
			t.Errorf("WriteFile(%q) = %v", p, err)
		}
		if _, err := g.FS.Open(ctx, tid, agentfs.RootHome, p); !errors.Is(err, agentfs.ErrEscapes) {
			t.Errorf("Open(%q) = %v", p, err)
		}
	}
	if err := g.FS.MkdirAll(ctx, tid, agentfs.RootHome, "dir-link/x"); !errors.Is(err, agentfs.ErrEscapes) {
		t.Errorf("MkdirAll through symlink = %v", err)
	}
	if err := g.FS.Remove(ctx, tid, agentfs.RootHome, "dir-link/passwd", false); !errors.Is(err, agentfs.ErrEscapes) {
		t.Errorf("Remove through symlink = %v", err)
	}
	for _, p := range []string{"../secret/passwd", "/etc/passwd", "a/../../x", `a\b`, "a\x00b"} {
		if _, err := g.FS.ReadFile(ctx, tid, agentfs.RootHome, p); !errors.Is(err, agentfs.ErrInvalidPath) {
			t.Errorf("ReadFile(%q) = %v", p, err)
		}
	}
	if _, err := g.FS.Lstat(ctx, tid, "etc", ""); !errors.Is(err, agentfs.ErrUnknownRoot) {
		t.Errorf("unknown root = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(secret, "passwd")); string(b) != "root:x:0:0" {
		t.Fatal("the file outside the root changed")
	}
	if _, err := g.FS.ReadFile(ctx, tid, agentfs.RootHome, "missing.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file = %v", err)
	}
	if err := g.FS.MkdirAll(ctx, tid, agentfs.RootWorkspace, "a/b/c"); err != nil {
		t.Fatal(err)
	}
	roots := g.FS.Roots(ctx, tid)
	if len(roots) != 2 || roots[1].GuestPath != g.Home {
		t.Fatalf("roots = %+v", roots)
	}
}

func TestWakeTimeoutAndContext(t *testing.T) {
	g, _ := start(t)
	g.Waker.Set(time.Hour, nil)
	_, err := g.FS.Lstat(context.Background(), tid, agentfs.RootHome, "")
	if !errors.Is(err, agentfs.ErrUnavailable) || !strings.Contains(err.Error(), "did not start within 2s") {
		t.Fatalf("wake timeout = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.FS.Lstat(ctx, tid, agentfs.RootHome, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
	if g.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatal("lease leaked")
	}
	// Roots never wakes the machine.
	wakes := g.Waker.Wakes()
	g.FS.Roots(context.Background(), tid)
	if g.Waker.Wakes() != wakes {
		t.Fatal("Roots woke the machine")
	}
}

// A file request does not start a machine that the user stopped: it fails
// with agentfs.ErrStopped (an expected 503).
func TestStoppedMachineIsNotStarted(t *testing.T) {
	g, _ := start(t)
	g.Waker.Set(0, agentfs.ErrStopped)
	_, err := g.FS.Lstat(context.Background(), tid, agentfs.RootHome, "")
	if !errors.Is(err, agentfs.ErrStopped) || !errors.Is(err, agentfs.ErrUnavailable) {
		t.Fatalf("request to a stopped machine = %v", err)
	}
	if g.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatal("lease leaked")
	}
}

// A file request whose wait ends while the start continues fails with
// agentfs.ErrStarting (a 503 that the client sends again).
func TestStartingMachineAnswersStarting(t *testing.T) {
	g, _ := start(t)
	g.Waker.Set(0, agentfs.ErrStarting)
	_, err := g.FS.Lstat(context.Background(), tid, agentfs.RootHome, "")
	if !errors.Is(err, agentfs.ErrStarting) || !errors.Is(err, agentfs.ErrUnavailable) {
		t.Fatalf("request while the machine starts = %v", err)
	}
	if g.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatal("lease leaked")
	}
}
