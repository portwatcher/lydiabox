package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/portwatcher/lydiabox/internal/config"
)

// MagpieSecretStore removes the magpie secrets of a tenant (*store.Store).
type MagpieSecretStore interface {
	DeleteMagpieSecrets(ctx context.Context, tenantID string) (int64, error)
}

// purgeGatewayState removes, in guest-native mode, what the host model
// gateway of an earlier mode left: the magpie data directory (sign-ins,
// API keys, caller keys), the magpie cache, and the magpie secrets of the
// state database. Both paths come from the configuration only. A path that
// is a symbolic link, has a symbolic link between it and the data
// directory, or is not a directory is left as it is. A start that finds
// nothing does nothing. Outside guest-native mode it does nothing.
func purgeGatewayState(ctx context.Context, cfg *config.Config, st MagpieSecretStore, log *slog.Logger) error {
	if cfg == nil || !cfg.GuestNative() {
		return nil
	}
	var bytes, dirs int64
	var errs []error
	for _, dir := range []string{cfg.MagpieDir(), cfg.MagpieCacheDir()} {
		n, removed, err := removeDataSubdir(cfg.DataDir, dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if removed {
			bytes += n
			dirs++
		}
	}
	var rows int64
	if st != nil {
		n, err := st.DeleteMagpieSecrets(ctx, cfg.TenantID)
		if err != nil {
			errs = append(errs, err)
		}
		rows = n
	}
	if dirs > 0 || rows > 0 {
		log.Info("removed the host model gateway data of an earlier mode (guest-native mode)",
			"directories", dirs, "bytes", bytes, "secret_rows", rows)
	}
	return errors.Join(errs...)
}

// removeDataSubdir removes dir when it is a real directory below dataDir
// with no symbolic link on the way, and returns the size of its regular
// files. A missing directory is not an error.
func removeDataSubdir(dataDir, dir string) (int64, bool, error) {
	base, err := filepath.Abs(filepath.Clean(dataDir))
	if err != nil {
		return 0, false, err
	}
	target, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return 0, false, err
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return 0, false, fmt.Errorf("guest-native cleanup: a path is not below the data directory")
	}
	// Every step below the data directory must be a real directory.
	p := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return 0, false, fmt.Errorf("guest-native cleanup: a gateway data path is not a real directory; it was not changed")
		}
	}
	var size int64
	_ = filepath.WalkDir(target, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	if err := os.RemoveAll(target); err != nil {
		return 0, false, fmt.Errorf("guest-native cleanup: %w", err)
	}
	return size, true, nil
}
