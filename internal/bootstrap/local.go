package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// builtLocally reports whether the manifest builds the binary on the host
// for this platform and no mirror download replaces it.
func (b *Bootstrapper) builtLocally(name string) bool {
	bin, ok := b.opts.Manifest.Lookup(name)
	if !ok {
		return false
	}
	if m, ok := b.opts.Mirrors[name]; ok && m.URL != "" {
		return false
	}
	return bin.Platforms[b.Platform()].BuiltLocally()
}

// InstallLocal installs a binary that was built on the host (for example
// virtiofsd for linux/arm64 from deploy/virtiofsd/build.sh) into BinDir and
// records the manifest version and the file SHA256, so a later manifest
// upgrade reports it as outdated. It returns the installed path.
func (b *Bootstrapper) InstallLocal(name, src string) (string, error) {
	bin, ok := b.opts.Manifest.Lookup(name)
	if !ok {
		return "", fmt.Errorf("bootstrap: unknown binary %q", name)
	}
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("bootstrap: %w", err)
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return "", fmt.Errorf("bootstrap: %s is not a regular, non-empty file", src)
	}
	if err := os.MkdirAll(b.opts.BinDir, 0o755); err != nil {
		return "", fmt.Errorf("bootstrap: create %s: %w", b.opts.BinDir, err)
	}
	dest := filepath.Join(b.opts.BinDir, name)
	tmp, err := os.CreateTemp(b.opts.BinDir, "."+name+".local-*")
	if err != nil {
		return "", fmt.Errorf("bootstrap: %w", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		tmp.Close()
		return "", fmt.Errorf("bootstrap: copy %s: %w", src, err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return "", fmt.Errorf("bootstrap: %w", err)
	}
	art := bin.Platforms[b.Platform()]
	origin := "local:" + src
	if art.Build != "" {
		origin = "build:" + art.Build
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if err := b.writeRecord(name, installRecord{Version: bin.Version, SHA256: sum, URL: origin}); err != nil {
		return "", err
	}
	b.opts.Logger.Info("installed a locally built runtime binary", "name", name, "version", bin.Version, "path", dest, "sha256", sum)
	return dest, nil
}
