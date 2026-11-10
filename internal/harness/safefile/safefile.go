// Package safefile reads and writes files below a trusted root directory
// that untrusted code (the agent VM) can also modify. Every path is
// resolved through symlinks and must stay inside the root, so a guest
// symlink such as ~/.claude/settings.json -> /etc/passwd cannot make the
// host read or write outside /home/agent or /workspace. Writes are atomic
// (temporary file, fsync, rename).
package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// GuestUID and GuestGID own files that a root host process creates in the
// guest trees.
const (
	GuestUID = 1000
	GuestGID = 1000
)

// ErrEscapes is returned when a path resolves outside its root.
var ErrEscapes = errors.New("path escapes its root")

// Resolve returns the real path of rel below root. Missing trailing
// components are allowed; every existing component is resolved through
// symlinks and checked against the real root.
func Resolve(root, rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." {
		rel = ""
	}
	if rel != "" && !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %q", ErrEscapes, rel)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %s: %w", root, err)
	}
	if rel == "" {
		return realRoot, nil
	}
	return resolveBelow(realRoot, filepath.Join(realRoot, rel))
}

func resolveBelow(realRoot, p string) (string, error) {
	real, err := filepath.EvalSymlinks(p)
	if err == nil {
		if !within(realRoot, real) {
			return "", fmt.Errorf("%w: %s", ErrEscapes, p)
		}
		return real, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if fi, lerr := os.Lstat(p); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
		// A dangling symlink: refuse, its target could be created later.
		return "", fmt.Errorf("%w: dangling symlink %s", ErrEscapes, p)
	}
	parent := filepath.Dir(p)
	if parent == p || !within(realRoot, parent) {
		return "", fmt.Errorf("%w: %s", ErrEscapes, p)
	}
	realParent, err := resolveBelow(realRoot, parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(realParent, filepath.Base(p)), nil
}

func within(root, p string) bool {
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ReadFile reads rel below root.
func ReadFile(root, rel string) ([]byte, error) {
	p, err := Resolve(root, rel)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Exists reports whether rel exists below root (symlinks that escape count
// as missing).
func Exists(root, rel string) bool {
	p, err := Resolve(root, rel)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// MkdirAll creates rel and its parents below root and returns its real path.
func MkdirAll(root, rel string) (string, error) {
	p, err := Resolve(root, rel)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	var missing []string
	for cur := p; cur != realRoot; cur = filepath.Dir(cur) {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		missing = append(missing, cur)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		chownGuest(missing[i])
	}
	// Re-check: a concurrent guest could have swapped a directory for a link.
	return Resolve(root, rel)
}

// WriteFile atomically writes data to rel below root, creating parent
// directories. An existing file keeps its mode; a new file gets perm.
func WriteFile(root, rel string, data []byte, perm os.FileMode) error {
	dirRel := filepath.Dir(filepath.Clean(filepath.FromSlash(rel)))
	if _, err := MkdirAll(root, dirRel); err != nil {
		return err
	}
	// Resolve the file itself: a symlink inside the root is written
	// through, one that leaves the root is refused.
	target, err := Resolve(root, rel)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if fi, err := os.Stat(target); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("write %s: not a regular file", target)
		}
		perm = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".lydiabox-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	chownGuest(tmp)
	if err := os.Rename(tmp, target); err != nil {
		return err
	}
	ok = true
	return nil
}

// RemoveAll removes rel below root. Missing paths are not an error.
func RemoveAll(root, rel string) error {
	if filepath.Clean(filepath.FromSlash(rel)) == "." {
		return fmt.Errorf("%w: refusing to remove the root", ErrEscapes)
	}
	p, err := Resolve(root, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.RemoveAll(p)
}

// chownGuest gives a file created by a root host process to the guest user.
func chownGuest(p string) {
	if os.Geteuid() == 0 {
		_ = os.Lchown(p, GuestUID, GuestGID)
	}
}
