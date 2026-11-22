//go:build unix

package rpc

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// The socket mode is set without following a symlink that the guest put
// in place of the socket.
func TestChmodSocketDoesNotFollowSymlink(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "host-9001.sock")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := chmodSocket(link, 0o666); err == nil {
		t.Fatal("chmodSocket accepted a symlink")
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Fatalf("symlink target mode changed to %v", fi.Mode().Perm())
	}
	sock := filepath.Join(dir, "s.sock")
	ln, err := ListenUnix(sock, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if fi, err := os.Lstat(sock); err != nil || fi.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode %v %v", fi.Mode().Perm(), err)
	}
	_ = net.Listener(ln)
}
