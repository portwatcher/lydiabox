package bootstrap

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrChecksumMismatch is returned when a download does not match its pinned SHA256.
var ErrChecksumMismatch = errors.New("bootstrap: SHA256 checksum mismatch")

// fetchAndInstall downloads art into a temporary file next to the target,
// verifies its SHA256, extracts the executable, and renames it into place so
// a crash never leaves a partial binary at BinDir/name.
func (b *Bootstrapper) fetchAndInstall(ctx context.Context, name string, art Artifact) error {
	dl, err := os.CreateTemp(b.opts.BinDir, ".download-"+name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(dl.Name())
	defer dl.Close()

	sum, err := b.download(ctx, art.URL, dl)
	if err != nil {
		return err
	}
	if !strings.EqualFold(sum, art.SHA256) {
		return fmt.Errorf("%w: %s: expected %s, got %s", ErrChecksumMismatch, art.URL, strings.ToLower(art.SHA256), sum)
	}
	if _, err := dl.Seek(0, io.SeekStart); err != nil {
		return err
	}

	exe, err := os.CreateTemp(b.opts.BinDir, ".install-"+name+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(exe.Name())
	defer exe.Close()

	limit := b.opts.MaxDownloadBytes * 4
	switch art.Archive {
	case ArchiveRaw, "":
		_, err = copyLimited(exe, dl, limit)
	case ArchiveTarGz:
		err = extractTarGz(dl, art.Member, exe, limit)
	case ArchiveZip:
		err = extractZip(dl, art.Member, exe, limit)
	default:
		err = fmt.Errorf("unknown archive format %q", art.Archive)
	}
	if err != nil {
		return err
	}
	if err := exe.Chmod(0o755); err != nil {
		return err
	}
	if err := exe.Sync(); err != nil {
		return err
	}
	if err := exe.Close(); err != nil {
		return err
	}
	return os.Rename(exe.Name(), filepath.Join(b.opts.BinDir, name))
}

// download streams rawURL into w and returns the hex SHA256 of the bytes.
// http, https, and file URLs are supported (file URLs serve air-gapped mirrors).
func (b *Bootstrapper) download(ctx context.Context, rawURL string, w io.Writer) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	var body io.ReadCloser
	switch u.Scheme {
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", "lydiabox-bootstrap")
		resp, err := b.opts.HTTPClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("GET %s: %w", rawURL, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("GET %s: unexpected status %s", rawURL, resp.Status)
		}
		body = resp.Body
	case "file":
		f, err := os.Open(u.Path)
		if err != nil {
			return "", err
		}
		body = f
	default:
		return "", fmt.Errorf("unsupported URL scheme %q in %s", u.Scheme, rawURL)
	}
	defer body.Close()

	h := sha256.New()
	if _, err := copyLimited(io.MultiWriter(w, h), body, b.opts.MaxDownloadBytes); err != nil {
		return "", fmt.Errorf("download %s: %w", rawURL, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyLimited copies at most limit bytes and fails if src holds more.
func copyLimited(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return n, err
	}
	if n > limit {
		return n, fmt.Errorf("content exceeds the %d byte limit", limit)
	}
	return n, nil
}

func memberMatches(entry, member string) bool {
	entry = strings.TrimPrefix(path.Clean(strings.TrimPrefix(entry, "./")), "/")
	member = strings.TrimPrefix(path.Clean(member), "/")
	if entry == member {
		return true
	}
	return !strings.Contains(member, "/") && path.Base(entry) == member
}

func extractTarGz(r io.Reader, member string, w io.Writer, limit int64) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("archive has no regular file %q", member)
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !memberMatches(hdr.Name, member) {
			continue
		}
		_, err = copyLimited(w, tr, limit)
		return err
	}
}

func extractZip(f *os.File, member string, w io.Writer, limit int64) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, zf := range zr.File {
		if !zf.Mode().IsRegular() || !memberMatches(zf.Name, member) {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		_, err = copyLimited(w, rc, limit)
		rc.Close()
		return err
	}
	return fmt.Errorf("archive has no regular file %q", member)
}
