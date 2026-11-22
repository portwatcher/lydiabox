//go:build unix

package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadFileRefusesFIFO(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(root, "fifo")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ReadFile of a FIFO must fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFile blocked on a FIFO")
	}
}

func TestReadFileRefusesHugeFile(t *testing.T) {
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, "big"))
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: no disk space is used.
	if err := f.Truncate(MaxReadBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadFile(root, "big"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadFile = %v, want ErrTooLarge", err)
	}
}
