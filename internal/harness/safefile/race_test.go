package safefile

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// swapper replaces root/dir with a symlink to target and back, in a loop,
// like a guest process that races the host between check and use.
func swapper(t *testing.T, root, dir, target string) (stop func()) {
	t.Helper()
	d := filepath.Join(root, dir)
	real := d + ".real"
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if os.Rename(d, real) == nil {
				_ = os.Symlink(target, d)
				runtime.Gosched()
				_ = os.Remove(d)
				_ = os.Rename(real, d)
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

const raceRounds = 1000

func TestWriteFileRaceStaysInRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	stop := swapper(t, root, "conf", outside)
	for i := 0; i < raceRounds; i++ {
		_ = WriteFile(root, "conf/config.toml", []byte("x = 1\n"), 0o600)
	}
	stop()
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("WriteFile wrote outside the root: %v", entries)
	}
}

func TestReadFileRaceStaysInRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conf", "config.toml"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "config.toml"), []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := swapper(t, root, "conf", outside)
	defer stop()
	for i := 0; i < raceRounds; i++ {
		if b, err := ReadFile(root, "conf/config.toml"); err == nil && string(b) == "host secret" {
			t.Fatalf("ReadFile read a file outside the root after %d rounds", i)
		}
	}
}

func TestRemoveAllRaceStaysInRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "victim")
	if err := os.Mkdir(filepath.Join(root, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	stop := swapper(t, root, "sessions", outside)
	for i := 0; i < raceRounds; i++ {
		_ = os.MkdirAll(filepath.Join(root, "sessions.real", "victim"), 0o755)
		_ = RemoveAll(root, "sessions/victim")
		if _, err := os.Stat(victim); err != nil {
			stop()
			t.Fatalf("RemoveAll removed a directory outside the root after %d rounds", i)
		}
	}
	stop()
}
