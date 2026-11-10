package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testDirs struct{ home, workspace string }

func (d testDirs) HomeDir(string) (string, error)      { return d.home, nil }
func (d testDirs) WorkspaceDir(string) (string, error) { return d.workspace, nil }

// newSandbox returns a Service over two roots plus a sibling "secret"
// directory outside both roots that escape attempts target.
func newSandbox(t *testing.T) (*Service, testDirs, string) {
	t.Helper()
	base := t.TempDir()
	d := testDirs{home: filepath.Join(base, "jfs", "home", "agent"), workspace: filepath.Join(base, "jfs", "workspace")}
	secret := filepath.Join(base, "secret")
	for _, p := range []string{d.home, d.workspace, secret} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(secret, "passwd"), []byte("root:x:0:0"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewService(d, Options{MaxTextBytes: 1024}), d, secret
}

func TestCleanRel(t *testing.T) {
	ok := map[string]string{"": ".", ".": ".", "a": "a", "a/b/../c": "", "a//b/": filepath.Join("a", "b"), "./a": "a", "a/.": "a"}
	for in, want := range ok {
		got, err := CleanRel(in)
		if want == "" {
			if err == nil {
				t.Errorf("CleanRel(%q) must fail", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("CleanRel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"..", "../x", "a/../../x", "/etc/passwd", "/", "a\\b", "..\\x", "a\x00b", "x/.."} {
		if _, err := CleanRel(bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanRel(%q) = %v, want ErrInvalidPath", bad, err)
		}
	}
}

func TestTraversalRejected(t *testing.T) {
	svc, d, secret := newSandbox(t)
	const tid = "default"

	// Symlinks that point outside the root: absolute, relative, and a directory.
	os.Symlink(filepath.Join(secret, "passwd"), filepath.Join(d.workspace, "abs-link"))
	os.Symlink("../../secret/passwd", filepath.Join(d.workspace, "rel-link"))
	os.Symlink(secret, filepath.Join(d.workspace, "dir-link"))
	// A symlink that stays inside the root is allowed.
	os.WriteFile(filepath.Join(d.workspace, "real.txt"), []byte("ok"), 0o644)
	os.Symlink("real.txt", filepath.Join(d.workspace, "inside-link"))

	for _, p := range []string{"abs-link", "rel-link", "dir-link/passwd"} {
		if _, err := svc.ReadText(tid, RootWorkspace, p); !errors.Is(err, ErrEscapes) {
			t.Errorf("ReadText(%q) = %v, want ErrEscapes", p, err)
		}
		if f, _, err := svc.OpenFile(tid, RootWorkspace, p); !errors.Is(err, ErrEscapes) {
			if f != nil {
				f.Close()
			}
			t.Errorf("OpenFile(%q) = %v, want ErrEscapes", p, err)
		}
	}
	if _, _, err := svc.List(tid, RootWorkspace, "dir-link"); !errors.Is(err, ErrEscapes) {
		t.Errorf("List through symlink = %v", err)
	}
	if _, err := svc.WriteText(tid, RootWorkspace, "dir-link/passwd", "pwned", nil); !errors.Is(err, ErrEscapes) {
		t.Errorf("WriteText through symlink = %v", err)
	}
	if _, err := svc.Upload(tid, RootWorkspace, "dir-link", "x.txt", strings.NewReader("pwned"), true, -1); !errors.Is(err, ErrEscapes) {
		t.Errorf("Upload through symlink = %v", err)
	}
	if _, err := svc.Mkdir(tid, RootWorkspace, "dir-link/new", true); !errors.Is(err, ErrEscapes) {
		t.Errorf("Mkdir through symlink = %v", err)
	}
	if _, err := svc.Rename(tid, RootWorkspace, "real.txt", "dir-link/stolen.txt", false); !errors.Is(err, ErrEscapes) {
		t.Errorf("Rename into symlink = %v", err)
	}
	if err := svc.Delete(tid, RootWorkspace, "dir-link/passwd", false); !errors.Is(err, ErrEscapes) {
		t.Errorf("Delete through symlink = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(secret, "passwd")); string(data) != "root:x:0:0" {
		t.Fatal("secret file was modified")
	}
	if _, err := os.Stat(filepath.Join(secret, "stolen.txt")); !os.IsNotExist(err) {
		t.Fatal("file escaped the root")
	}

	// Deleting the escaping symlink removes the link, not the target.
	if err := svc.Delete(tid, RootWorkspace, "dir-link", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(secret, "passwd")); err != nil {
		t.Fatal("recursive delete followed a symlink")
	}

	if tf, err := svc.ReadText(tid, RootWorkspace, "inside-link"); err != nil || tf.Content != "ok" {
		t.Fatalf("inside symlink = %+v, %v", tf, err)
	}
	for _, p := range []string{"../secret/passwd", "/etc/passwd", "a/../../secret"} {
		if _, err := svc.Stat(tid, RootWorkspace, p); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Stat(%q) = %v", p, err)
		}
	}
	// The home root cannot reach the workspace root through "..".
	if _, err := svc.Stat(tid, RootHome, "../../workspace/real.txt"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("cross-root traversal = %v", err)
	}
	if _, err := svc.Stat(tid, "etc", ""); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("unknown root = %v", err)
	}
}

func TestTextReadWrite(t *testing.T) {
	svc, d, _ := newSandbox(t)
	e, err := svc.WriteText("default", RootHome, "notes.md", "hello", nil)
	if err != nil || e.Size != 5 || e.Path != "notes.md" {
		t.Fatalf("write = %+v, %v", e, err)
	}
	os.Chmod(filepath.Join(d.home, "notes.md"), 0o600)
	tf, err := svc.ReadText("default", RootHome, "notes.md")
	if err != nil || tf.Content != "hello" {
		t.Fatalf("read = %+v, %v", tf, err)
	}
	stale := tf.ModTime.Add(-time.Hour)
	if _, err := svc.WriteText("default", RootHome, "notes.md", "x", &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write = %v", err)
	}
	e, err = svc.WriteText("default", RootHome, "notes.md", "hello world", &tf.ModTime)
	if err != nil || e.Mode != "0600" {
		t.Fatalf("write with base = %+v, %v (mode must be preserved)", e, err)
	}
	if _, err := svc.WriteText("default", RootHome, "missing/notes.md", "x", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("write without parent = %v", err)
	}
	if _, err := svc.WriteText("default", RootHome, "", "x", nil); !errors.Is(err, ErrIsRoot) {
		t.Fatalf("write root = %v", err)
	}
	if _, err := svc.WriteText("default", RootHome, "big", strings.Repeat("x", 2000), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("big write = %v", err)
	}
	os.WriteFile(filepath.Join(d.home, "bin"), []byte{0xff, 0xfe, 0x00}, 0o644)
	if _, err := svc.ReadText("default", RootHome, "bin"); !errors.Is(err, ErrNotText) {
		t.Fatalf("binary read = %v", err)
	}
	os.WriteFile(filepath.Join(d.home, "huge"), make([]byte, 2048), 0o644)
	if _, err := svc.ReadText("default", RootHome, "huge"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("huge read = %v", err)
	}
	entries, _ := os.ReadDir(d.home)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

func TestMutations(t *testing.T) {
	svc, d, _ := newSandbox(t)
	tid := "default"
	if _, err := svc.Mkdir(tid, RootWorkspace, "a/b/c", false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("mkdir without parents = %v", err)
	}
	if _, err := svc.Mkdir(tid, RootWorkspace, "a/b/c", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Mkdir(tid, RootWorkspace, "a/b/c", true); err != nil {
		t.Fatal("mkdir -p on an existing dir must succeed")
	}
	if _, err := svc.Mkdir(tid, RootWorkspace, "a", false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("mkdir existing = %v", err)
	}
	os.WriteFile(filepath.Join(d.workspace, "a", "f.txt"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(d.workspace, "g.txt"), []byte("2"), 0o644)

	if _, err := svc.Rename(tid, RootWorkspace, "g.txt", "a/f.txt", false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rename onto existing = %v", err)
	}
	if e, err := svc.Rename(tid, RootWorkspace, "g.txt", "a/f.txt", true); err != nil || e.Path != "a/f.txt" {
		t.Fatalf("rename overwrite = %+v, %v", e, err)
	}
	if data, _ := os.ReadFile(filepath.Join(d.workspace, "a", "f.txt")); string(data) != "2" {
		t.Fatal("overwrite did not replace content")
	}
	if _, err := svc.Rename(tid, RootWorkspace, "a", "a/b/moved", false); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("move into itself = %v", err)
	}
	if _, err := svc.Rename(tid, RootWorkspace, "", "x", false); !errors.Is(err, ErrIsRoot) {
		t.Fatalf("rename root = %v", err)
	}
	if _, err := svc.Rename(tid, RootWorkspace, "a", "z", false); err != nil {
		t.Fatal(err)
	}

	if err := svc.Delete(tid, RootWorkspace, "z", false); err == nil {
		t.Fatal("deleting a non-empty dir without recursive must fail")
	}
	if err := svc.Delete(tid, RootWorkspace, "z", true); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(tid, RootWorkspace, "", true); !errors.Is(err, ErrIsRoot) {
		t.Fatalf("delete root = %v", err)
	}
	if err := svc.Delete(tid, RootWorkspace, "nope", false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("delete missing = %v", err)
	}

	res, err := svc.Upload(tid, RootWorkspace, "up", "deep/dir/file.bin", strings.NewReader("data"), false, 100)
	if err != nil || res.Path != "up/deep/dir/file.bin" || res.Replaced {
		t.Fatalf("upload = %+v, %v", res, err)
	}
	if _, err := svc.Upload(tid, RootWorkspace, "up", "deep/dir/file.bin", strings.NewReader("x"), false, 100); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("upload conflict = %v", err)
	}
	if res, err := svc.Upload(tid, RootWorkspace, "up", "deep/dir/file.bin", strings.NewReader("x"), true, 100); err != nil || !res.Replaced {
		t.Fatalf("upload overwrite = %+v, %v", res, err)
	}
	if _, err := svc.Upload(tid, RootWorkspace, "", "big", strings.NewReader("12345"), false, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("upload limit = %v", err)
	}
	if _, err := os.Stat(filepath.Join(d.workspace, "big")); !os.IsNotExist(err) {
		t.Fatal("oversized upload must not leave a file")
	}

	self, list, err := svc.List(tid, RootWorkspace, "")
	if err != nil || self.Type != "dir" || len(list) != 1 || list[0].Name != "up" || list[0].Type != "dir" {
		t.Fatalf("list = %+v %+v %v", self, list, err)
	}
	if _, _, err := svc.List(tid, RootWorkspace, "up/deep/dir/file.bin"); !errors.Is(err, ErrNotDir) {
		t.Fatalf("list file = %v", err)
	}
}
