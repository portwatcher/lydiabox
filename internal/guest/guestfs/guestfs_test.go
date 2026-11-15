//go:build unix

package guestfs

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs"
)

func newService(t *testing.T) (*Service, string, string) {
	t.Helper()
	base := t.TempDir()
	home, ws, secret := filepath.Join(base, "data", "home"), filepath.Join(base, "data", "workspace"), filepath.Join(base, "secret")
	for _, d := range []string{home, ws, secret} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(secret, "key"), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Home: home, Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	return s, home, secret
}

func TestNewValidatesRoots(t *testing.T) {
	if _, err := New(Options{Home: "relative/home"}); err == nil {
		t.Fatal("a relative root must be refused")
	}
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r := s.Roots(); r[0].GuestPath != "/workspace" || r[1].GuestPath != "/home/agent" {
		t.Fatalf("default roots = %+v", r)
	}
	if os.Geteuid() != 0 {
		if _, err := New(Options{As: &Identity{UID: 1000, GID: 1000}}); err == nil {
			t.Fatal("an identity switch without root must be refused")
		}
	}
}

func TestOperations(t *testing.T) {
	s, home, _ := newService(t)
	if _, err := s.Mkdir(agentfs.RootHome, "a/b", true); err != nil {
		t.Fatal(err)
	}
	res, err := s.Write(agentfs.RootHome, "a/b/f.txt", strings.NewReader("hello"), agentfs.WriteOptions{})
	if err != nil || res.Size != 5 || res.Path != "a/b/f.txt" {
		t.Fatalf("write = %+v, %v", res, err)
	}
	f, e, err := s.Open(agentfs.RootHome, "a/b/f.txt")
	if err != nil || e.Size != 5 {
		t.Fatalf("open = %+v, %v", e, err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello" {
		t.Fatalf("content %q", b)
	}
	self, entries, err := s.List(agentfs.RootHome, "a/b")
	if err != nil || self.Type != "dir" || len(entries) != 1 || entries[0].Name != "f.txt" {
		t.Fatalf("list = %+v %+v %v", self, entries, err)
	}
	if e, err := s.Rename(agentfs.RootHome, "a/b/f.txt", "g.txt", false); err != nil || e.Path != "g.txt" {
		t.Fatalf("rename = %+v, %v", e, err)
	}
	if e, err := s.Lstat(agentfs.RootHome, "g.txt"); err != nil || e.Type != "file" {
		t.Fatalf("stat = %+v, %v", e, err)
	}
	if data, err := s.ReadFile(agentfs.RootHome, "g.txt", 3); !errors.Is(err, agentfs.ErrTooLarge) {
		t.Fatalf("read over the limit = %q, %v", data, err)
	}
	if err := s.WriteFile(agentfs.RootHome, "x/y/z.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(home, "x", "y", "z.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("writefile mode %v %v", fi, err)
	}
	if err := s.Remove(agentfs.RootHome, "a", false); err == nil {
		t.Fatal("removing a non-empty directory without recursive must fail")
	}
	if err := s.Remove(agentfs.RootHome, "a", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(agentfs.RootHome, "", true); !errors.Is(err, agentfs.ErrIsRoot) {
		t.Fatalf("remove root = %v", err)
	}
	if err := s.MkdirAll(agentfs.RootWorkspace, "p/q"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lstat("system", ""); !errors.Is(err, agentfs.ErrUnknownRoot) {
		t.Fatalf("unknown root = %v", err)
	}
}

func TestTraversalAndSymlinks(t *testing.T) {
	s, home, secret := newService(t)
	os.Symlink(filepath.Join(secret, "key"), filepath.Join(home, "abs"))
	os.Symlink("../../secret/key", filepath.Join(home, "rel"))
	os.Symlink(secret, filepath.Join(home, "dir"))
	os.Symlink(filepath.Join(home, "inside.txt"), filepath.Join(home, "abs-inside"))
	os.WriteFile(filepath.Join(home, "inside.txt"), []byte("ok"), 0o644)

	for _, p := range []string{"../secret/key", "/etc/passwd", "a/../../secret/key", `..\secret`, "a\x00"} {
		if _, err := s.Lstat(agentfs.RootHome, p); !errors.Is(err, agentfs.ErrInvalidPath) {
			t.Errorf("Lstat(%q) = %v", p, err)
		}
	}
	for _, p := range []string{"abs", "rel", "dir/key"} {
		if _, _, err := s.Open(agentfs.RootHome, p); !errors.Is(err, agentfs.ErrEscapes) {
			t.Errorf("Open(%q) = %v", p, err)
		}
		if _, err := s.ReadFile(agentfs.RootHome, p, 1<<20); !errors.Is(err, agentfs.ErrEscapes) {
			t.Errorf("ReadFile(%q) = %v", p, err)
		}
		if err := s.WriteFile(agentfs.RootHome, p, []byte("pwned"), 0o644); !errors.Is(err, agentfs.ErrEscapes) {
			t.Errorf("WriteFile(%q) = %v", p, err)
		}
	}
	if _, err := s.Write(agentfs.RootHome, "dir/new", strings.NewReader("x"), agentfs.WriteOptions{}); !errors.Is(err, agentfs.ErrEscapes) {
		t.Errorf("Write through a symlink = %v", err)
	}
	if _, _, err := s.List(agentfs.RootHome, "dir"); !errors.Is(err, agentfs.ErrEscapes) {
		t.Errorf("List through a symlink = %v", err)
	}
	if _, err := s.Rename(agentfs.RootHome, "inside.txt", "dir/stolen", false); !errors.Is(err, agentfs.ErrEscapes) {
		t.Errorf("Rename into a symlink = %v", err)
	}
	if err := s.MkdirAll(agentfs.RootHome, "dir/sub"); !errors.Is(err, agentfs.ErrEscapes) {
		t.Errorf("MkdirAll through a symlink = %v", err)
	}
	// An absolute symlink that points inside the root is followed by
	// WriteFile; Remove deletes a link, never its target.
	if err := s.WriteFile(agentfs.RootHome, "abs-inside", []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "inside.txt")); string(b) != "new" {
		t.Fatalf("absolute inside link: %q", b)
	}
	if err := s.Remove(agentfs.RootHome, "dir", true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(secret, "key")); string(b) != "PRIVATE" {
		t.Fatal("the file outside the root changed")
	}
	entries, _ := os.ReadDir(secret)
	if len(entries) != 1 {
		t.Fatalf("files escaped into the secret directory: %v", entries)
	}
	// A symlink loop ends with an error.
	os.Symlink("loop-b", filepath.Join(home, "loop-a"))
	os.Symlink("loop-a", filepath.Join(home, "loop-b"))
	if err := s.WriteFile(agentfs.RootHome, "loop-a", []byte("x"), 0o644); err == nil {
		t.Fatal("a symlink loop must fail")
	}
}

func TestWriteKeepsModeAndRefusesNonRegular(t *testing.T) {
	s, home, _ := newService(t)
	os.WriteFile(filepath.Join(home, "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	if _, err := s.Write(agentfs.RootHome, "run.sh", bytes.NewReader([]byte("echo\n")), agentfs.WriteOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(home, "run.sh")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	os.Mkdir(filepath.Join(home, "d"), 0o755)
	if _, err := s.Write(agentfs.RootHome, "d", strings.NewReader("x"), agentfs.WriteOptions{Overwrite: true}); !errors.Is(err, agentfs.ErrIsDir) {
		t.Fatalf("write over a directory = %v", err)
	}
	os.Symlink("run.sh", filepath.Join(home, "link"))
	if _, err := s.Write(agentfs.RootHome, "link", strings.NewReader("x"), agentfs.WriteOptions{Overwrite: true, RegularOnly: true}); !errors.Is(err, agentfs.ErrNotRegular) {
		t.Fatalf("regular-only write over a symlink = %v", err)
	}
	if _, err := s.Write(agentfs.RootHome, "new.txt", strings.NewReader("x"), agentfs.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(agentfs.RootHome, "new.txt", strings.NewReader("y"), agentfs.WriteOptions{}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("write without overwrite = %v", err)
	}
}
