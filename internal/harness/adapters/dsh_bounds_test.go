package adapters

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// A guest-written zstd file that expands beyond the limit is refused, so
// host-side discovery cannot be made to allocate without bound.
func TestReadDSHBoundsDecompressedSize(t *testing.T) {
	old := maxDSHDecoded
	maxDSHDecoded = 1 << 20
	defer func() { maxDSHDecoded = old }()

	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(bytes.Repeat([]byte("\n"), 4<<20)); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	rel := ".dsh/sessions/a/b/session.jsonl.zst"
	if err := os.MkdirAll(filepath.Join(home, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, rel), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDSH(home, rel); !errors.Is(err, safefile.ErrTooLarge) {
		t.Fatalf("readDSH = %v, want ErrTooLarge", err)
	}
}
