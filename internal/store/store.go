// Package store is the host SQLite state database (modernc.org/sqlite, no
// CGO). It holds connector credentials (AES-256-GCM encrypted), per-harness
// model bindings, the audit log, and harness session metadata. Every public
// method takes a tenantID so a multi-tenant control plane can share it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store is the host state database handle. It is safe for concurrent use.
type Store struct {
	db     *sql.DB
	cipher *Cipher
	path   string
	now    func() time.Time
}

// Open opens (creating if needed) the database at path, applies pending
// migrations, and uses masterKey for secret encryption.
func Open(ctx context.Context, path string, masterKey []byte) (*Store, error) {
	c, err := NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create dir: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	dsn := "file:" + filepath.ToSlash(path) + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(4)
	s := &Store{db: db, cipher: c, path: path, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: secure %s: %w", path, err)
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Cipher exposes the secret cipher for subsystems that encrypt their own blobs.
func (s *Store) Cipher() *Cipher { return s.cipher }

// VacuumInto writes a consistent, compacted snapshot of the database to dest,
// which must not exist. It is used by the metadata backup ticker.
func (s *Store) VacuumInto(ctx context.Context, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("store: vacuum target %s already exists", dest)
	}
	if strings.ContainsRune(dest, 0) {
		return errors.New("store: invalid vacuum target")
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("store: vacuum into %s: %w", dest, err)
	}
	return nil
}

func checkTenant(tenantID string) error {
	if tenantID == "" {
		return errors.New("store: tenantID is required")
	}
	return nil
}

func (s *Store) timestamp() int64 { return s.now().UnixMilli() }

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
