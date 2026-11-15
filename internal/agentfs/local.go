package agentfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// Dirs resolves the host directories of a tenant (storage.Provider
// implements it in juicefs mode).
type Dirs interface {
	HomeDir(tenantID string) (string, error)
	WorkspaceDir(tenantID string) (string, error)
}

// Owner is a uid/gid pair.
type Owner struct{ UID, GID int }

// LocalOptions configures Local.
type LocalOptions struct {
	// Owner, when set, receives ownership of created files and directories.
	// When nil and the process runs as root, the guest user 1000:1000 is
	// used.
	Owner *Owner
}

// Local is the FS of the host JuiceFS mount (juicefs storage mode). Every
// File API operation resolves paths through an os.Root opened on the root
// directory, so "..", absolute paths, and symlinks that leave the root are
// rejected by the lookup itself. ReadFile, WriteFile, and MkdirAll use
// internal/harness/safefile, as the wirers did before this seam existed.
type Local struct {
	dirs  Dirs
	owner *Owner
}

// NewLocal returns the FS of the host directories in dirs.
func NewLocal(dirs Dirs, opts LocalOptions) *Local {
	if opts.Owner == nil && os.Geteuid() == 0 {
		opts.Owner = &Owner{UID: 1000, GID: 1000}
	}
	return &Local{dirs: dirs, owner: opts.Owner}
}

// Roots implements FS.
func (l *Local) Roots(context.Context, string) []RootInfo {
	return []RootInfo{{Name: RootWorkspace, GuestPath: DefaultGuestWorkspace}, {Name: RootHome, GuestPath: DefaultGuestHome}}
}

// Dir returns the host directory of a root.
func (l *Local) Dir(tenantID string, root Root) (string, error) {
	switch root {
	case RootWorkspace:
		return l.dirs.WorkspaceDir(tenantID)
	case RootHome:
		return l.dirs.HomeDir(tenantID)
	}
	return "", fmt.Errorf("%w %q", ErrUnknownRoot, root)
}

// openRoot opens the os.Root of a named root. Opening per call keeps the
// handle valid across JuiceFS remounts.
func (l *Local) openRoot(tenantID string, root Root) (*os.Root, error) {
	dir, err := l.Dir(tenantID, root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open root %s: %w", root, err)
	}
	return r, nil
}

func (l *Local) withRoot(tenantID string, root Root, p string, fn func(r *os.Root, rel string) error) error {
	rel, err := CleanRel(p)
	if err != nil {
		return err
	}
	r, err := l.openRoot(tenantID, root)
	if err != nil {
		return err
	}
	defer r.Close()
	return MapRootErr(fn(r, rel))
}

// Lstat implements FS.
func (l *Local) Lstat(_ context.Context, tenantID string, root Root, p string) (Entry, error) {
	var e Entry
	err := l.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		fi, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		e = EntryFrom(rel, fi)
		return nil
	})
	return e, err
}

// List implements FS.
func (l *Local) List(_ context.Context, tenantID string, root Root, p string) (Entry, []Entry, error) {
	var (
		self    Entry
		entries []Entry
	)
	err := l.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		var err error
		self, entries, err = ListRoot(r, rel)
		return err
	})
	return self, entries, err
}

// ListRoot lists rel inside r (shared with the guest file service).
func ListRoot(r *os.Root, rel string) (Entry, []Entry, error) {
	f, err := r.Open(rel)
	if err != nil {
		return Entry{}, nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Entry{}, nil, err
	}
	if !fi.IsDir() {
		return Entry{}, nil, ErrNotDir
	}
	des, err := f.ReadDir(-1)
	if err != nil {
		return Entry{}, nil, err
	}
	entries := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue // removed between ReadDir and Info
		}
		entries = append(entries, EntryFrom(filepath.Join(rel, de.Name()), info))
	}
	return EntryFrom(rel, fi), entries, nil
}

// localFile is an *os.File with its Entry.
type localFile struct {
	*os.File
	info Entry
}

func (f localFile) Info() Entry { return f.info }

// Open implements FS.
func (l *Local) Open(_ context.Context, tenantID string, root Root, p string) (File, error) {
	var file File
	err := l.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		f, e, err := OpenRegular(r, rel)
		if err != nil {
			return err
		}
		file = localFile{File: f, info: e}
		return nil
	})
	return file, err
}

// OpenRegular opens a regular file inside r for reading.
func OpenRegular(r *os.Root, rel string) (*os.File, Entry, error) {
	f, err := r.Open(rel)
	if err != nil {
		return nil, Entry{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, Entry{}, err
	}
	if fi.IsDir() {
		f.Close()
		return nil, Entry{}, ErrIsDir
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, Entry{}, ErrNotRegular
	}
	return f, EntryFrom(rel, fi), nil
}

// Write implements FS.
func (l *Local) Write(_ context.Context, tenantID string, root Root, p string, src io.Reader, o WriteOptions) (WriteResult, error) {
	var res WriteResult
	err := l.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		var err error
		res, err = WriteRoot(r, rel, src, o, l.chownFn())
		return err
	})
	return res, err
}

// WriteRoot performs an atomic write of rel inside r (shared with the guest
// file service). chown, when not nil, is applied to created files and
// directories.
func WriteRoot(r *os.Root, rel string, src io.Reader, o WriteOptions, chown func(*os.File) error) (WriteResult, error) {
	var res WriteResult
	if rel == "." {
		return res, ErrIsRoot
	}
	if o.MkdirParents {
		if err := MkdirAllRoot(r, filepath.Dir(rel), chown); err != nil {
			return res, err
		}
	}
	mode := o.Mode.Perm()
	if mode == 0 {
		mode = 0o644
	}
	if fi, err := r.Lstat(rel); err == nil {
		if fi.IsDir() {
			return res, ErrIsDir
		}
		if o.RegularOnly && !fi.Mode().IsRegular() {
			return res, ErrNotRegular
		}
		if !o.Overwrite {
			return res, fmt.Errorf("%w: %s", fs.ErrExist, filepath.ToSlash(rel))
		}
		if o.IfModTime != nil && !fi.ModTime().Equal(*o.IfModTime) {
			return res, ErrConflict
		}
		res.Replaced = true
		if fi.Mode().IsRegular() {
			mode = fi.Mode().Perm()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	if err := writeAtomic(r, rel, src, mode, o.Limit, chown); err != nil {
		return res, err
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return res, err
	}
	res.Entry = EntryFrom(rel, fi)
	return res, nil
}

// writeAtomic streams src into a temporary file in the target directory and
// renames it over rel. limit < 0 disables the size check.
func writeAtomic(r *os.Root, rel string, src io.Reader, mode fs.FileMode, limit int64, chown func(*os.File) error) error {
	dir, base := filepath.Split(rel)
	if dir == "" {
		dir = "."
	}
	tmpRel := filepath.Join(dir, fmt.Sprintf(".%s.lydiabox-%d.tmp", base, time.Now().UnixNano()))
	f, err := r.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			f.Close()
			r.Remove(tmpRel)
		}
	}()
	if limit >= 0 {
		src = io.LimitReader(src, limit+1)
	}
	n, err := io.Copy(f, src)
	if err != nil {
		return err
	}
	if limit >= 0 && n > limit {
		return ErrTooLarge
	}
	if chown != nil {
		if err := chown(f); err != nil {
			return err
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := renameIn(r, tmpRel, r, rel); err != nil {
		return err
	}
	committed = true
	return nil
}

func (l *Local) chown(f *os.File) error {
	if l.owner == nil {
		return nil
	}
	return f.Chown(l.owner.UID, l.owner.GID)
}

func (l *Local) chownFn() func(*os.File) error {
	if l.owner == nil {
		return nil
	}
	return l.chown
}

// ReadFile implements FS.
func (l *Local) ReadFile(_ context.Context, tenantID string, root Root, rel string) ([]byte, error) {
	dir, err := l.Dir(tenantID, root)
	if err != nil {
		return nil, err
	}
	b, err := safefile.ReadFile(dir, rel)
	return b, mapSafeErr(err)
}

// WriteFile implements FS.
func (l *Local) WriteFile(_ context.Context, tenantID string, root Root, rel string, data []byte, perm fs.FileMode) error {
	dir, err := l.Dir(tenantID, root)
	if err != nil {
		return err
	}
	return mapSafeErr(safefile.WriteFile(dir, rel, data, perm))
}

// MkdirAll implements FS.
func (l *Local) MkdirAll(_ context.Context, tenantID string, root Root, rel string) error {
	dir, err := l.Dir(tenantID, root)
	if err != nil {
		return err
	}
	_, err = safefile.MkdirAll(dir, rel)
	return mapSafeErr(err)
}

func mapSafeErr(err error) error {
	if errors.Is(err, safefile.ErrEscapes) {
		return fmt.Errorf("%w: %v", ErrEscapes, err)
	}
	return err
}

// splitRel splits an OS-relative path into its components.
func splitRel(rel string) []string { return strings.Split(rel, string(filepath.Separator)) }
