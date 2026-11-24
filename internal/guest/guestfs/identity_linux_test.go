//go:build linux

package guestfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/portwatcher/lydiabox/internal/agentfs"
)

// TestRunsAsAgentUser needs root on Linux (run it in a container: see
// docs/conformance.md). File operations must use the agent identity: new
// files belong to it, and files only root can read stay unreadable.
func TestRunsAsAgentUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	const uid, gid = 4242, 4242
	base := t.TempDir()
	// The agent identity must be able to reach the roots.
	for _, d := range []string{filepath.Dir(base), base} {
		if err := os.Chmod(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home, ws := filepath.Join(base, "home"), filepath.Join(base, "ws")
	for _, d := range []string{home, ws} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(d, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "root-only"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "root-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Home: home, Workspace: ws, As: &Identity{UID: uid, GID: gid}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(agentfs.RootHome, "new.txt", strings.NewReader("x"), agentfs.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteFile(agentfs.RootHome, "cfg/settings.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"new.txt", "cfg", "cfg/settings.json"} {
		fi, err := os.Stat(filepath.Join(home, p))
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if st.Uid != uid || st.Gid != gid {
			t.Errorf("%s is owned by %d:%d, want %d:%d", p, st.Uid, st.Gid, uid, gid)
		}
	}
	if _, err := s.ReadFile(agentfs.RootHome, "root-only", 1<<20); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("read of a root-only file = %v", err)
	}
	if _, err := s.Write(agentfs.RootHome, "root-dir/x", strings.NewReader("x"), agentfs.WriteOptions{}); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("write into a root-owned directory = %v", err)
	}
	// The identity of the thread is restored: root can still read.
	if _, err := os.ReadFile(filepath.Join(home, "root-only")); err != nil {
		t.Fatal(err)
	}
}
