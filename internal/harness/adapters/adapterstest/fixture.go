// Package adapterstest builds a host /home/agent tree with realistic
// session logs of all five harnesses (internal/harness/adapters/testdata)
// for tests of the adapters, the manager, and the bridge.
package adapterstest

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

// Native IDs of the fixture sessions.
const (
	ClaudeID       = "6b1e2f4a-8c3d-4e5f-9a7b-1c2d3e4f5a6b"
	CodexID        = "019a6f2c-3b1d-7c0e-9f4a-2d8e5b6c7a10"
	CodexLegacyID  = "5f1c9a2e-7b3d-4c1e-8f2a-9d0e1f2a3b4c"
	DSHID          = "session-a91f3c2e-5d6b-4e7f-8a9b-0c1d2e3f4a5b"
	AntigravityID  = "c41d7e8a-2b3c-4d5e-8f90-a1b2c3d4e5f6"
	OpenCodeID     = "ses_5a1f0c2b3ffeA1b2C3d4E5f6G7"
	OpenCodeLegacy = "ses_4f00aa11bffeLegacy0000001"
	Workspace      = "/workspace/lydiabox"
)

// TestdataDir is the adapters testdata directory.
func TestdataDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

// Home copies the fixture home into a temporary directory, builds the
// SQLite databases of opencode and Antigravity from their SQL dumps, and
// returns the host path that stands for /home/agent.
func Home(t testing.TB) string {
	t.Helper()
	src := filepath.Join(TestdataDir(), "home")
	dst := filepath.Join(t.TempDir(), "home", "agent")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	BuildDB(t, filepath.Join(TestdataDir(), "opencode.sql"), filepath.Join(dst, ".local/share/opencode/opencode.db"))
	BuildDB(t, filepath.Join(TestdataDir(), "antigravity.sql"),
		filepath.Join(dst, ".gemini/antigravity/conversations", AntigravityID+".db"))
	return dst
}

// BuildDB creates a SQLite database at dst from the statements in sqlFile.
func BuildDB(t testing.TB, sqlFile, dst string) {
	t.Helper()
	script, err := os.ReadFile(sqlFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dst))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range strings.Split(string(script), ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v\n%s", sqlFile, err, stmt)
		}
	}
}
