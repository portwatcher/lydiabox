package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// GuestUID and GuestGID own the provisioned directories (user agent in the guest).
const (
	GuestUID = 1000
	GuestGID = 1000
)

// provision creates home/agent, workspace, and system on the mounted volume
// and the sparse ext4 root-upper.img that persists the guest root overlay.
func (s *Supervisor) provision(ctx context.Context) error {
	asRoot := os.Geteuid() == 0
	for _, dir := range []string{s.cfg.HomeDir(), s.cfg.WorkspaceDir(), s.cfg.SystemDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("storage: create %s: %w", dir, err)
		}
		if asRoot {
			if err := s.opts.Chown(dir, GuestUID, GuestGID); err != nil {
				return fmt.Errorf("storage: chown %s: %w", dir, err)
			}
		}
	}
	if asRoot {
		// home/ itself must be traversable by the guest user.
		if err := s.opts.Chown(filepath.Dir(s.cfg.HomeDir()), GuestUID, GuestGID); err != nil {
			return fmt.Errorf("storage: chown home: %w", err)
		}
	}
	err := s.ensureRootUpper(ctx)
	if err != nil && !s.opts.RequireRootUpper {
		s.log.Warn("root-upper.img is not available; guest root changes will not persist", "err", err)
		return nil
	}
	return err
}

// ensureRootUpper creates system/root-upper.img when it does not exist. The
// image is built under a temporary name and renamed after mkfs succeeds, so
// an interrupted run never leaves a half-formatted image in place.
func (s *Supervisor) ensureRootUpper(ctx context.Context) error {
	img := s.cfg.RootUpperImage()
	if _, err := os.Stat(img); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: stat %s: %w", img, err)
	}
	mkfs := s.opts.MkfsBinary
	if !filepath.IsAbs(mkfs) {
		p, err := exec.LookPath(mkfs)
		if err != nil {
			return fmt.Errorf("storage: %s not found: %w", mkfs, err)
		}
		mkfs = p
	}
	tmp := img + ".partial"
	os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("storage: create %s: %w", tmp, err)
	}
	if err := f.Truncate(s.opts.RootUpperSize); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("storage: allocate %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := s.opts.Runner.Run(ctx, MkfsExt4(mkfs, tmp)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("storage: format root-upper.img: %w", err)
	}
	if err := os.Rename(tmp, img); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("storage: install root-upper.img: %w", err)
	}
	s.log.Info("created sparse root-upper.img", "path", img, "bytes", s.opts.RootUpperSize)
	return nil
}

// MkfsExt4 returns the command that formats image as ext4.
func MkfsExt4(binary, image string) Command {
	return Command{Path: binary, Args: []string{"-F", "-q", image}}
}
