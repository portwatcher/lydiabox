package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// sqliteHeader starts every SQLite 3 database file.
var sqliteHeader = []byte("SQLite format 3\x00")

// RestoreState restores the host state database (lydiabox.db) of a tenant
// from its S3 snapshot when dbPath does not exist. It must run before the
// store opens. It reports whether a snapshot was restored; a missing
// snapshot is not an error (first start). Encrypted connector tokens in the
// snapshot decrypt only with the master key that wrote them.
func RestoreState(ctx context.Context, objects ObjectStore, tenantID, dbPath string, log *slog.Logger) (bool, error) {
	if log == nil {
		log = slog.Default()
	}
	if _, err := os.Stat(dbPath); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("storage: state database: %w", err)
	}
	key := StateBackupKey(tenantID)
	rc, err := objects.Get(ctx, key)
	if errors.Is(err, ErrObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: check state snapshot %s: %w", key, err)
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return false, fmt.Errorf("storage: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dbPath), ".lydiabox.db.restore-*")
	if err != nil {
		return false, fmt.Errorf("storage: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := gunzipTo(tmp, rc)
	if err != nil {
		return false, fmt.Errorf("storage: restore state snapshot %s: %w", key, err)
	}
	if err := tmp.Sync(); err != nil {
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return false, err
	}
	if err := os.Rename(tmp.Name(), dbPath); err != nil {
		return false, fmt.Errorf("storage: %w", err)
	}
	log.Info("restored the host state database from S3; stored connector tokens decrypt only with the master key that wrote them",
		"key", key, "path", dbPath, "bytes", n)
	return true, nil
}

// gunzipTo decompresses src into dst and checks the SQLite file header.
func gunzipTo(dst io.Writer, src io.Reader) (int64, error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	head := make([]byte, len(sqliteHeader))
	if _, err := io.ReadFull(gz, head); err != nil || !bytes.Equal(head, sqliteHeader) {
		return 0, errors.New("the snapshot is not a SQLite database")
	}
	if _, err := dst.Write(head); err != nil {
		return 0, err
	}
	n, err := io.Copy(dst, gz)
	return n + int64(len(head)), err
}
