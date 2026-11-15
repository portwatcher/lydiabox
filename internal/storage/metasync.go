package storage

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// syncLoop uploads metadata snapshots every MetaSyncInterval. A snapshot is
// uploaded only when its content hash changed since the last upload, so an
// idle volume costs one local dump per tick and no S3 writes.
func (s *Supervisor) syncLoop(ctx context.Context) {
	defer close(s.syncDone)
	t := time.NewTicker(s.cfg.MetaSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.SyncNow(ctx, s.tenant); err != nil && ctx.Err() == nil {
				s.log.Error("metadata sync failed", "err", err)
			}
		}
	}
}

// SyncNow dumps JuiceFS metadata and snapshots the state database, uploading
// each compressed snapshot to S3 when it changed.
func (s *Supervisor) SyncNow(ctx context.Context, tenantID string) error {
	if err := s.checkTenant(tenantID); err != nil {
		return err
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	uploaded := false
	var errs []error
	up, err := s.uploader().upload(ctx, MetaBackupKey(s.tenant), func(w io.Writer) error {
		cmd := s.jfs.Dump()
		cmd.Stdout = w
		return s.opts.Runner.Run(ctx, cmd)
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("juicefs metadata: %w", err))
	}
	uploaded = uploaded || up

	if s.opts.State != nil {
		up, err := s.uploader().upload(ctx, StateBackupKey(s.tenant), stateSnapshot(ctx, s.opts.State, s.cfg.TmpDir()))
		if err != nil {
			errs = append(errs, fmt.Errorf("state database: %w", err))
		}
		uploaded = uploaded || up
	}

	err = errors.Join(errs...)
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if err != nil {
		s.status.LastSyncError = err.Error()
		return err
	}
	now := time.Now().UTC()
	s.status.LastSyncAt = now
	s.status.LastSyncError = ""
	if uploaded {
		s.status.LastUploadAt = now
	}
	return nil
}

func (s *Supervisor) uploader() *snapshotUploader {
	return &snapshotUploader{objects: s.opts.Objects, tmpDir: s.cfg.TmpDir(), log: s.log, sums: s.sums}
}

// stateSnapshot writes a consistent copy of the host state database.
func stateSnapshot(ctx context.Context, state Snapshotter, tmpDir string) func(io.Writer) error {
	return func(w io.Writer) error {
		if err := os.MkdirAll(tmpDir, 0o700); err != nil {
			return err
		}
		dir, err := os.MkdirTemp(tmpDir, "state-snapshot-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		dest := filepath.Join(dir, "lydiabox.db")
		if err := state.VacuumInto(ctx, dest); err != nil {
			return err
		}
		f, err := os.Open(dest)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	}
}

// snapshotUploader uploads compressed snapshots when their content changed.
// sums holds the SHA256 of the last upload per key; the caller serializes
// uploads.
type snapshotUploader struct {
	objects ObjectStore
	tmpDir  string
	log     *slog.Logger
	sums    map[string]string
}

// upload gzips the output of produce into a temporary file and uploads it
// to key when the SHA256 of the uncompressed content differs from the last
// successful upload. It reports whether an upload happened.
func (u *snapshotUploader) upload(ctx context.Context, key string, produce func(io.Writer) error) (bool, error) {
	if err := os.MkdirAll(u.tmpDir, 0o700); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(u.tmpDir, ".snapshot-*.gz")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	h := sha256.New()
	gz := gzip.NewWriter(tmp)
	if err := produce(io.MultiWriter(gz, h)); err != nil {
		return false, err
	}
	if err := gz.Close(); err != nil {
		return false, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if u.sums[key] == sum {
		return false, nil
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err != nil {
		return false, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if err := u.objects.Put(ctx, key, tmp, size, "application/gzip"); err != nil {
		return false, err
	}
	u.sums[key] = sum
	u.log.Info("uploaded metadata snapshot", "key", key, "bytes", size)
	return true, nil
}

// logWriter forwards child process output to a structured logger, one record
// per line.
type logWriter struct {
	mu   sync.Mutex
	log  *slog.Logger
	name string
	buf  bytes.Buffer
}

func newLogWriter(l *slog.Logger, name string) *logWriter {
	return &logWriter{log: l, name: name}
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadBytes('\n')
		if err != nil {
			// Keep the partial line for the next write; cap runaway lines.
			if len(line) > 64<<10 {
				line = line[:64<<10]
			}
			w.buf.Reset()
			w.buf.Write(line)
			return len(p), nil
		}
		sc := bufio.NewScanner(bytes.NewReader(line))
		for sc.Scan() {
			if t := sc.Text(); t != "" {
				w.log.Info(t, "process", w.name)
			}
		}
	}
}
