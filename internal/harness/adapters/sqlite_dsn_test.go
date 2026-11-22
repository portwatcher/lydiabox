package adapters

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A guest directory name must not add driver options or SQL to the
// SQLite connection that the host opens on a guest database.
func TestOpenOCDBPathCannotInjectPragma(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "escaped.db")
	dir := "o?_pragma=user_version%3BATTACH '" + strings.ReplaceAll(target, "/", "%2F") + "' AS z%3BCREATE TABLE z.t(a)&a="
	share := filepath.Join(home, ".local", "share")
	if err := os.MkdirAll(filepath.Join(share, dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, "o"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, dir, "opencode.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(share, "opencode")); err != nil {
		t.Fatal(err)
	}
	_, _ = OpenCode{}.DiscoverSessions(home)
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("host created %s outside the guest home", target)
	}
}

// A '?' in a directory name is part of the path: the host opens the file
// in that directory.
func TestSQLiteDSNKeepsQuestionMarkInPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b#c%d e")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "x.db")
	db, err := sql.Open("sqlite", sqliteDSN(p, url.Values{}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t(a)"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("database not at %s: %v", p, err)
	}
}
