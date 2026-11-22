package agentfs

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// ErrCredential is returned when guest-native mode keeps the host away
// from a credential file of a harness.
var ErrCredential = fmt.Errorf("%w: a sign-in or key file of a harness stays in the VM", fs.ErrPermission)

// CredentialPaths names the files that the host must not read or write.
// Files are relative to the home root; Names are base names that are
// refused in every root and directory; Dirs are home directories whose
// whole tree is refused.
type CredentialPaths struct {
	Files []string
	Names []string
	Dirs  []string
}

// Guard returns fsys with the reads and writes of the credential files
// refused (guest-native mode, docs/design.md section 4.4.2). Listing,
// stat, and removal stay: they do not move the content through the host.
// A read of a symbolic link is refused too, because the link can point
// at a credential file.
func Guard(fsys FS, c CredentialPaths) FS {
	return &guardFS{FS: fsys, c: c}
}

type guardFS struct {
	FS
	c CredentialPaths
}

func (g *guardFS) denied(root Root, rel string) bool {
	p := path.Clean(filepath.ToSlash(rel))
	for _, n := range g.c.Names {
		if path.Base(p) == n {
			return true
		}
	}
	if root != RootHome {
		return false
	}
	for _, f := range g.c.Files {
		if p == f {
			return true
		}
	}
	for _, d := range g.c.Dirs {
		if p == d || strings.HasPrefix(p, d+"/") {
			return true
		}
	}
	return false
}

// checkRead refuses a credential path and a symbolic link.
func (g *guardFS) checkRead(ctx context.Context, tenantID string, root Root, rel string) error {
	if g.denied(root, rel) {
		return fmt.Errorf("%s: %w", rel, ErrCredential)
	}
	e, err := g.FS.Lstat(ctx, tenantID, root, rel)
	if err == nil && e.Type == "symlink" {
		return fmt.Errorf("%s is a symbolic link: %w", rel, ErrCredential)
	}
	return nil
}

func (g *guardFS) Open(ctx context.Context, tenantID string, root Root, rel string) (File, error) {
	if err := g.checkRead(ctx, tenantID, root, rel); err != nil {
		return nil, err
	}
	return g.FS.Open(ctx, tenantID, root, rel)
}

func (g *guardFS) ReadFile(ctx context.Context, tenantID string, root Root, rel string) ([]byte, error) {
	if err := g.checkRead(ctx, tenantID, root, rel); err != nil {
		return nil, err
	}
	return g.FS.ReadFile(ctx, tenantID, root, rel)
}

func (g *guardFS) Write(ctx context.Context, tenantID string, root Root, rel string, src io.Reader, o WriteOptions) (WriteResult, error) {
	if g.denied(root, rel) {
		return WriteResult{}, fmt.Errorf("%s: %w", rel, ErrCredential)
	}
	return g.FS.Write(ctx, tenantID, root, rel, src, o)
}

func (g *guardFS) WriteFile(ctx context.Context, tenantID string, root Root, rel string, data []byte, perm fs.FileMode) error {
	if g.denied(root, rel) {
		return fmt.Errorf("%s: %w", rel, ErrCredential)
	}
	return g.FS.WriteFile(ctx, tenantID, root, rel, data, perm)
}

// holdsCredential reports whether the home directory rel contains a
// credential file or directory.
func (g *guardFS) holdsCredential(root Root, rel string) bool {
	if root != RootHome {
		return false
	}
	p := path.Clean(filepath.ToSlash(rel))
	if p == "." {
		return true
	}
	for _, f := range append(append([]string{}, g.c.Files...), g.c.Dirs...) {
		if strings.HasPrefix(f, p+"/") {
			return true
		}
	}
	return false
}

func (g *guardFS) Rename(ctx context.Context, tenantID string, root Root, from, to string, overwrite bool) (Entry, error) {
	if g.denied(root, from) || g.denied(root, to) || g.holdsCredential(root, from) || g.holdsCredential(root, to) {
		return Entry{}, fmt.Errorf("%s: %w", from, ErrCredential)
	}
	return g.FS.Rename(ctx, tenantID, root, from, to, overwrite)
}

// Unwrap returns the guarded FS.
func (g *guardFS) Unwrap() FS { return g.FS }
