package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReadAndModes(t *testing.T) {
	root := t.TempDir()
	if err := WriteFile(root, "a/b/c.json", []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(root, "a/b/c.json")
	if err != nil || string(got) != "one" {
		t.Fatalf("read: %q %v", got, err)
	}
	if err := os.Chmod(filepath.Join(root, "a/b/c.json"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "a/b/c.json", []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "a/b/c.json"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode not preserved: %v %v", fi.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "a/b"))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "new"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	cases := []string{"link", "dir/x", "dir/secret", "../x", "dangling"}
	for _, rel := range cases {
		if _, err := ReadFile(root, rel); err == nil {
			t.Errorf("ReadFile(%q) escaped", rel)
		}
		if err := WriteFile(root, rel, []byte("x"), 0o644); !errors.Is(err, ErrEscapes) {
			t.Errorf("WriteFile(%q) = %v, want ErrEscapes", rel, err)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "host secret" {
		t.Fatal("secret was modified")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("dangling symlink target was created")
	}
	// An inside symlink is fine.
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(root, "alias/f", []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "real/f")); string(b) != "ok" {
		t.Fatal("write through inside symlink failed")
	}
	if err := RemoveAll(root, "."); err == nil {
		t.Fatal("removing the root must fail")
	}
	if err := RemoveAll(root, "real"); err != nil || Exists(root, "real") {
		t.Fatalf("RemoveAll: %v", err)
	}
}
