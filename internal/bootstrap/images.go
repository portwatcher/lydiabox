package bootstrap

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GuestImages are the files that `lydiabox runtime build-rootfs` produces and
// that LYDIABOX_ROOTFS_URL serves.
var GuestImages = []string{"vmlinux", "initramfs.cpio.gz", "rootfs.squashfs"}

// ImagesPresent reports whether every guest image exists in dir.
func ImagesPresent(dir string) bool {
	for _, name := range GuestImages {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
			return false
		}
	}
	return true
}

// FetchImages downloads the guest images from baseURL into dir. baseURL must
// serve SHA256SUMS (sha256sum format) next to the three images; every image is
// verified against it before it is moved into place. Existing images are
// replaced only after all downloads succeed.
func (b *Bootstrapper) FetchImages(ctx context.Context, baseURL, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("bootstrap: create %s: %w", dir, err)
	}
	base := strings.TrimRight(baseURL, "/")
	var sums bytes.Buffer
	if _, err := b.download(ctx, base+"/SHA256SUMS", &sums); err != nil {
		return fmt.Errorf("bootstrap: fetch image checksums: %w", err)
	}
	want, err := parseSHA256SUMS(sums.Bytes())
	if err != nil {
		return err
	}

	staged := map[string]string{}
	defer func() {
		for _, tmp := range staged {
			os.Remove(tmp)
		}
	}()
	for _, name := range GuestImages {
		sum, ok := want[name]
		if !ok {
			return fmt.Errorf("bootstrap: SHA256SUMS at %s does not list %s", base, name)
		}
		tmp, err := os.CreateTemp(dir, ".download-"+name+"-*")
		if err != nil {
			return err
		}
		staged[name] = tmp.Name()
		b.opts.Logger.Info("downloading guest image", "name", name, "url", base+"/"+name)
		got, err := b.download(ctx, base+"/"+name, tmp)
		closeErr := tmp.Close()
		if err != nil {
			return fmt.Errorf("bootstrap: %w", err)
		}
		if closeErr != nil {
			return closeErr
		}
		if !strings.EqualFold(got, sum) {
			return fmt.Errorf("%w: %s: expected %s, got %s", ErrChecksumMismatch, name, sum, got)
		}
	}
	for _, name := range GuestImages {
		if err := os.Rename(staged[name], filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("bootstrap: install %s: %w", name, err)
		}
		delete(staged, name)
	}
	return nil
}

func parseSHA256SUMS(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("bootstrap: malformed SHA256SUMS line %q", line)
		}
		out[filepath.Base(strings.TrimPrefix(fields[1], "*"))] = strings.ToLower(fields[0])
	}
	return out, sc.Err()
}
