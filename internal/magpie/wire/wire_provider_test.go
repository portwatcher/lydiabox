//go:build unix

package wire

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/agentfs/agentfstest"
)

// TestWirersProviderMatchesLocal applies every wirer to two copies of the
// test home: on a host directory (juicefs mode) and through the guest
// daemon file methods (provider mode). Both trees must end identical, and
// no upstream credential may appear in them.
func TestWirersProviderMatchesLocal(t *testing.T) {
	local := copyTree(t, "testdata/home")
	remote := copyTree(t, "testdata/home")
	g := agentfstest.Start(t, remote, t.TempDir())
	home := FSHome(context.Background(), g.FS, "default")
	for _, w := range All() {
		b := Binding{Model: "kimi/kimi-k3"}
		if err := w.Apply(DirHome(local), b); err != nil {
			t.Fatalf("%s local: %v", w.Harness(), err)
		}
		if err := w.Apply(home, b); err != nil {
			t.Fatalf("%s provider: %v", w.Harness(), err)
		}
		lm, err1 := w.CurrentModel(DirHome(local))
		rm, err2 := w.CurrentModel(home)
		if err1 != nil || err2 != nil || lm != rm {
			t.Fatalf("%s model local %q %v, provider %q %v", w.Harness(), lm, err1, rm, err2)
		}
	}
	want, got := snapshot(t, local), snapshot(t, remote)
	if len(want) != len(got) {
		t.Fatalf("local files %d, provider files %d", len(want), len(got))
	}
	for p, data := range want {
		if got[p] != data {
			t.Errorf("%s differs:\nlocal:\n%s\nprovider:\n%s", p, data, got[p])
		}
		if strings.Contains(got[p], "sk-") {
			t.Errorf("%s holds an upstream key", p)
		}
	}
	if g.Waker.Wakes() == 0 {
		t.Fatal("the provider wirer did not wake the machine")
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
