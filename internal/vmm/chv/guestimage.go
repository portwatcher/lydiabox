package chv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lydiabox/lydiabox/internal/storage"
)

// GuestImageLabel is the ext4 label of the injected guest binary image.
const GuestImageLabel = "lydiaguest"

// EnsureGuestImage builds (or reuses) a small read-only ext4 image holding
// /lydiabox-guest, attached as the second disk (/dev/vdb). The image is
// rebuilt when the binary changes, so upgrading lydiabox upgrades the guest
// daemon without rebuilding rootfs.squashfs.
func EnsureGuestImage(ctx context.Context, runner storage.Runner, mkfs, binary, image string) error {
	sum, size, err := fileSHA256(binary)
	if err != nil {
		return fmt.Errorf("chv: guest binary %s: %w", binary, err)
	}
	stamp := image + ".sha256"
	if b, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(b)) == sum {
		if fi, err := os.Stat(image); err == nil && fi.Size() > 0 {
			return nil
		}
	}
	dir := filepath.Dir(image)
	staging, err := os.MkdirTemp(dir, ".guest-staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := copyFile(binary, filepath.Join(staging, "lydiabox-guest"), 0o755); err != nil {
		return err
	}
	tmp := image + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	// ext4 metadata needs headroom; round up to a MiB.
	imgSize := (size*5/4 + (16 << 20) + (1 << 20) - 1) &^ ((1 << 20) - 1)
	if err := f.Truncate(imgSize); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := runner.Run(ctx, storage.Command{Path: mkfs, Args: []string{"-F", "-q", "-L", GuestImageLabel, "-d", staging, tmp}}); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("chv: build guest image: %w", err)
	}
	if err := os.Rename(tmp, image); err != nil {
		return err
	}
	return os.WriteFile(stamp, []byte(sum+"\n"), 0o644)
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
