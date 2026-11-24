package agentfs_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/portwatcher/lydiabox/internal/agentfs"
)

type dirs struct{ home, ws string }

func (d dirs) HomeDir(string) (string, error)      { return d.home, nil }
func (d dirs) WorkspaceDir(string) (string, error) { return d.ws, nil }

func TestGuardKeepsCredentialFilesFromTheHost(t *testing.T) {
	d := dirs{home: t.TempDir(), ws: t.TempDir()}
	for rel, data := range map[string]string{".codex/auth.json": "secret", ".codex/config.toml": "model = \"x\"\n", "skills/.credentials.json": "secret"} {
		p := filepath.Join(d.home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".codex/auth.json", filepath.Join(d.home, "link")); err != nil {
		t.Fatal(err)
	}
	g := agentfs.Guard(agentfs.NewLocal(d, agentfs.LocalOptions{}), agentfs.CredentialPaths{
		Files: []string{".codex/auth.json"}, Names: []string{".credentials.json"}, Dirs: []string{".config/gcloud"},
	})
	ctx := context.Background()
	const tid = "default"
	denied := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, agentfs.ErrCredential) || !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s: %v, want ErrCredential", what, err)
		}
	}
	_, err := g.ReadFile(ctx, tid, agentfs.RootHome, ".codex/auth.json")
	denied("read", err)
	_, err = g.Open(ctx, tid, agentfs.RootHome, "./.codex/../.codex/auth.json")
	denied("open", err)
	_, err = g.ReadFile(ctx, tid, agentfs.RootHome, "skills/.credentials.json")
	denied("read by name", err)
	_, err = g.ReadFile(ctx, tid, agentfs.RootHome, "link")
	denied("read a link", err)
	denied("write", g.WriteFile(ctx, tid, agentfs.RootHome, ".config/gcloud/creds", []byte("x"), 0o600))
	_, err = g.Write(ctx, tid, agentfs.RootHome, ".codex/auth.json", bytes.NewReader(nil), agentfs.WriteOptions{})
	denied("write stream", err)
	_, err = g.Rename(ctx, tid, agentfs.RootHome, ".codex", "copy", false)
	denied("rename the parent", err)
	if b, err := g.ReadFile(ctx, tid, agentfs.RootHome, ".codex/config.toml"); err != nil || len(b) == 0 {
		t.Errorf("config read: %q %v", b, err)
	}
	if _, kids, err := g.List(ctx, tid, agentfs.RootHome, ".codex"); err != nil || len(kids) != 2 {
		t.Errorf("list: %d %v", len(kids), err)
	}
}

// A directory link (alias -> .codex) must not open a way around the guard,
// and a file of a guarded directory is guarded except the allowed files.
func TestGuardRefusesLinkedParentsAndGuardedDirs(t *testing.T) {
	d := dirs{home: t.TempDir(), ws: t.TempDir()}
	for rel, data := range map[string]string{".codex/auth.json": "secret", ".gemini/app/token": "secret", ".gemini/app/settings.json": "{}"} {
		p := filepath.Join(d.home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".codex", filepath.Join(d.home, "alias")); err != nil {
		t.Fatal(err)
	}
	g := agentfs.Guard(agentfs.NewLocal(d, agentfs.LocalOptions{}), agentfs.CredentialPaths{
		Files: []string{".codex/auth.json"}, Dirs: []string{".gemini/app"}, Allow: []string{".gemini/app/settings.json"},
	})
	ctx := context.Background()
	const tid = "default"
	if _, err := g.ReadFile(ctx, tid, agentfs.RootHome, "alias/auth.json"); !errors.Is(err, agentfs.ErrCredential) {
		t.Errorf("read through a linked parent: %v, want ErrCredential", err)
	}
	if err := g.WriteFile(ctx, tid, agentfs.RootHome, "alias/auth.json", []byte("x"), 0o600); !errors.Is(err, agentfs.ErrCredential) {
		t.Errorf("write through a linked parent: %v, want ErrCredential", err)
	}
	if _, err := g.Rename(ctx, tid, agentfs.RootHome, "alias/auth.json", "out.json", false); !errors.Is(err, agentfs.ErrCredential) {
		t.Errorf("rename through a linked parent: %v, want ErrCredential", err)
	}
	if _, err := g.ReadFile(ctx, tid, agentfs.RootHome, ".gemini/app/token"); !errors.Is(err, agentfs.ErrCredential) {
		t.Errorf("read in a guarded dir: %v, want ErrCredential", err)
	}
	if b, err := g.ReadFile(ctx, tid, agentfs.RootHome, ".gemini/app/settings.json"); err != nil || string(b) != "{}" {
		t.Errorf("allowed file: %q %v", b, err)
	}
}
