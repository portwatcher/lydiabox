// Package safefile reads and writes files below a trusted root directory
// that untrusted code (the agent VM) can also modify. Every path is
// resolved through symlinks and must stay inside the root, so a guest
// symlink such as ~/.claude/settings.json -> /etc/passwd cannot make the
// host read or write outside /home/agent or /workspace. Writes are atomic
// (temporary file, fsync, rename).
package safefile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// MaxReadBytes bounds ReadFile: the guest controls the files, and a huge,
// sparse, or endless file must not exhaust host memory.
const MaxReadBytes = 256 << 20

// ErrTooLarge is returned by ReadFile for a file above MaxReadBytes.
var ErrTooLarge = errors.New("file too large")

// The functions below first call Resolve, which gives clear errors for a
// path that escapes the root. They then do the operation itself through
// an os.Root, which resolves every component relative to the root
// directory. A guest that swaps a directory for a symlink between the two
// steps can make the operation fail, but cannot move it outside the root.

func openRoot(root, rel string) (*os.Root, string, error) {
	if _, err := Resolve(root, rel); err != nil {
		return nil, "", err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	rel = filepath.Clean(filepath.FromSlash(rel))
	return r, rel, nil
}

// ReadFile reads the regular file rel below root. It refuses other file
// types (a FIFO would block) and files above MaxReadBytes.
func ReadFile(root, rel string) ([]byte, error) {
	r, rel, err := openRoot(root, rel)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.OpenFile(rel, os.O_RDONLY|nonBlock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("read %s: not a regular file", rel)
	}
	if fi.Size() > MaxReadBytes {
		return nil, fmt.Errorf("read %s: %w", rel, ErrTooLarge)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxReadBytes {
		return nil, fmt.Errorf("read %s: %w", rel, ErrTooLarge)
	}
	return b, nil
}

// Stat returns the file info of rel below root (symlinks inside the root
// are followed).
func Stat(root, rel string) (fs.FileInfo, error) {
	r, rel, err := openRoot(root, rel)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.Stat(rel)
}

// Exists reports whether rel exists below root (symlinks that escape count
// as missing).
func Exists(root, rel string) bool {
	_, err := Stat(root, rel)
	return err == nil
}

// MkdirAll creates rel and its parents below root and returns its real path.
func MkdirAll(root, rel string) (string, error) {
	r, clean, err := openRoot(root, rel)
	if err != nil {
		return "", err
	}
	defer r.Close()
	if err := mkdirAllIn(r, clean); err != nil {
		return "", err
	}
	return Resolve(root, rel)
}

func mkdirAllIn(r *os.Root, rel string) error {
	if rel == "." {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		err := r.Mkdir(cur, 0o755)
		if err == nil {
			chownGuestIn(r, cur)
			continue
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

// WriteFile atomically writes data to rel below root, creating parent
// directories. An existing file keeps its mode; a new file gets perm.
func WriteFile(root, rel string, data []byte, perm os.FileMode) error {
	r, clean, err := openRoot(root, rel)
	if err != nil {
		return err
	}
	defer r.Close()
	if clean == "." {
		return fmt.Errorf("write %s: is the root", root)
	}
	if err := mkdirAllIn(r, filepath.Dir(clean)); err != nil {
		return err
	}
	// A symlink inside the root is written through, one that leaves the
	// root is refused.
	target, err := leafTarget(r, root, clean)
	if err != nil {
		return err
	}
	if fi, err := r.Stat(target); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("write %s: not a regular file", target)
		}
		perm = fi.Mode().Perm()
	}
	tmp, f, err := createTemp(r, target)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = r.Remove(tmp)
		}
	}()
	if err := writeAll(f, data, perm); err != nil {
		return err
	}
	if err := r.Rename(tmp, target); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeAll(f *os.File, data []byte, perm os.FileMode) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil && os.Geteuid() == 0 {
		err = f.Chown(GuestUID, GuestGID)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func createTemp(r *os.Root, target string) (string, *os.File, error) {
	dir, base := filepath.Split(target)
	for i := 0; i < 100; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", nil, err
		}
		tmp := filepath.Join(dir, "."+base+".lydiabox-"+hex.EncodeToString(b[:]))
		f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return tmp, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("write %s: no free temporary name", target)
}

// leafTarget follows a symlink at rel (the last component only; os.Root
// resolves the parents) and returns the root-relative path to write.
func leafTarget(r *os.Root, root, rel string) (string, error) {
	for range 40 {
		fi, err := r.Lstat(rel)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return rel, nil
		}
		link, err := r.Readlink(rel)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(link) {
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				return "", err
			}
			l, err := filepath.Rel(realRoot, link)
			if err != nil || !filepath.IsLocal(l) {
				return "", fmt.Errorf("%w: %s", ErrEscapes, rel)
			}
			rel = l
			continue
		}
		next := filepath.Join(filepath.Dir(rel), link)
		if !filepath.IsLocal(next) {
			return "", fmt.Errorf("%w: %s", ErrEscapes, rel)
		}
		rel = next
	}
	return "", fmt.Errorf("%w: too many links at %s", ErrEscapes, rel)
}

// RemoveAll removes rel below root. Missing paths are not an error. A
// symlink at rel is removed itself, not its target.
func RemoveAll(root, rel string) error {
	if filepath.Clean(filepath.FromSlash(rel)) == "." {
		return fmt.Errorf("%w: refusing to remove the root", ErrEscapes)
	}
	r, clean, err := openRoot(root, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer r.Close()
	return r.RemoveAll(clean)
}

// chownGuestIn gives a directory created by a root host process to the
// guest user.
func chownGuestIn(r *os.Root, rel string) {
	if os.Geteuid() == 0 {
		_ = r.Lchown(rel, GuestUID, GuestGID)
	}
}
