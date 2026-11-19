package instaboximage

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// TestFilesMatchDockerfile: Files and the COPY lines of the Dockerfile
// name the same files, destinations, and modes, and the Docker build
// context has each file.
func TestFilesMatchDockerfile(t *testing.T) {
	df, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]File{}
	sc := bufio.NewScanner(bytes.NewReader(df))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || f[0] != "COPY" {
			continue
		}
		mode := fs.FileMode(0o644)
		args := f[1:]
		if len(args) > 0 && strings.HasPrefix(args[0], "--chmod=") {
			if args[0] != "--chmod=0755" {
				t.Fatalf("unexpected COPY flag %s", args[0])
			}
			mode = 0o755
			args = args[1:]
		}
		if len(args) != 2 || !strings.HasPrefix(args[0], "deploy/instabox/") {
			t.Fatalf("unexpected COPY line %q", sc.Text())
		}
		src := strings.TrimPrefix(args[0], "deploy/instabox/")
		got[src] = File{Src: src, Dest: args[1], Mode: mode}
	}
	want := map[string]File{}
	for _, f := range Files {
		want[f.Src] = f
	}
	for src, f := range want {
		if got[src] != f {
			t.Errorf("Files has %+v, the Dockerfile copies %+v", f, got[src])
		}
	}
	for src, f := range got {
		if _, ok := want[src]; !ok {
			t.Errorf("the Dockerfile copies %+v; add it to Files", f)
		}
	}
	ignore, err := os.ReadFile("Dockerfile.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range Files {
		if !strings.Contains(string(ignore), "!deploy/instabox/"+f.Src+"\n") {
			t.Errorf("Dockerfile.dockerignore does not let %s into the build context", f.Src)
		}
	}
}

func TestBundleIsDeterministic(t *testing.T) {
	a, err := System()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(embedded, Files)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Tar, b.Tar) || a.SHA256 != b.SHA256 {
		t.Fatal("two builds of the bundle differ")
	}
	sum := sha256.Sum256(a.Tar)
	if a.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("the bundle hash is not the SHA-256 of the archive")
	}

	tr := tar.NewReader(bytes.NewReader(a.Tar))
	var names []string
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Uid != 0 || h.Gid != 0 || h.ModTime.Unix() != 0 || h.Typeflag != tar.TypeReg {
			t.Fatalf("entry %s: uid %d gid %d mtime %v type %c", h.Name, h.Uid, h.Gid, h.ModTime, h.Typeflag)
		}
		names = append(names, h.Name)
		data, _ := io.ReadAll(tr)
		files[h.Name] = data
		if h.Name == ScriptName && h.Mode != 0o755 {
			t.Fatalf("the script has the mode %o", h.Mode)
		}
	}
	if !sort.StringsAreSorted(names) || len(names) != len(Files)+2 {
		t.Fatalf("entries %v", names)
	}
	for _, f := range Files {
		want, _ := fs.ReadFile(embedded, f.Src)
		if !bytes.Equal(files["files/"+f.Src], want) {
			t.Fatalf("files/%s differs from the embedded file", f.Src)
		}
	}
	if !strings.Contains(string(files["files.list"]), "0755 /usr/local/sbin/lydiabox-instabox-entry lydiabox-instabox-entry\n") {
		t.Fatalf("files.list:\n%s", files["files.list"])
	}

	// A change of one file gives another hash.
	m := fstest.MapFS{ScriptName: {Data: []byte("#!/bin/sh\n")}, "a": {Data: []byte("1")}}
	one, err := Build(m, []File{{Src: "a", Dest: "/etc/a", Mode: 0o644}})
	if err != nil {
		t.Fatal(err)
	}
	m["a"] = &fstest.MapFile{Data: []byte("2")}
	two, err := Build(m, []File{{Src: "a", Dest: "/etc/a", Mode: 0o644}})
	if err != nil {
		t.Fatal(err)
	}
	if one.SHA256 == two.SHA256 {
		t.Fatal("a changed file gives the same bundle hash")
	}
}
