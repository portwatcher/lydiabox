package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRestoreState(t *testing.T) {
	ctx := context.Background()
	objs := newMemObjects()
	dbPath := filepath.Join(t.TempDir(), "state", "lydiabox.db")

	// No snapshot: nothing to restore (first start).
	if ok, err := RestoreState(ctx, objs, "default", dbPath, discardLogger()); ok || err != nil {
		t.Fatalf("no snapshot: %v %v", ok, err)
	}

	db := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte("page"), 1024)...)
	objs.objs[StateBackupKey("default")] = gzipBytes(t, db)
	ok, err := RestoreState(ctx, objs, "default", dbPath, discardLogger())
	if !ok || err != nil {
		t.Fatalf("restore: %v %v", ok, err)
	}
	got, err := os.ReadFile(dbPath)
	if err != nil || !bytes.Equal(got, db) {
		t.Fatalf("restored content differs: %v", err)
	}
	if fi, _ := os.Stat(dbPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// An existing database is never replaced.
	objs.objs[StateBackupKey("default")] = gzipBytes(t, append([]byte("SQLite format 3\x00"), "newer"...))
	if ok, err := RestoreState(ctx, objs, "default", dbPath, discardLogger()); ok || err != nil {
		t.Fatalf("existing database: %v %v", ok, err)
	}
	if again, _ := os.ReadFile(dbPath); !bytes.Equal(again, db) {
		t.Fatal("existing database was replaced")
	}

	// Snapshots are per tenant, and a damaged snapshot leaves no file.
	other := filepath.Join(t.TempDir(), "lydiabox.db")
	if ok, _ := RestoreState(ctx, objs, "alice", other, discardLogger()); ok {
		t.Fatal("restored another tenant's snapshot")
	}
	objs.objs[StateBackupKey("alice")] = gzipBytes(t, []byte("not a database"))
	if _, err := RestoreState(ctx, objs, "alice", other, discardLogger()); err == nil {
		t.Fatal("a damaged snapshot must fail")
	}
	if entries, _ := os.ReadDir(filepath.Dir(other)); len(entries) != 0 {
		t.Fatalf("leftover files: %v", entries)
	}
}
