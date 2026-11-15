package agentfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Mkdir implements FS.
func (l *Local) Mkdir(_ context.Context, tenantID string, root Root, p string, parents bool) (Entry, error) {
	var e Entry
	err := l.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		var err error
		e, err = MkdirRoot(r, rel, parents, l.chownFn())
		return err
	})
	return e, err
}

// MkdirRoot creates rel inside r (shared with the guest file service).
func MkdirRoot(r *os.Root, rel string, parents bool, chown func(*os.File) error) (Entry, error) {
	if rel == "." {
		return Entry{}, ErrIsRoot
	}
	if parents {
		if err := MkdirAllRoot(r, rel, chown); err != nil {
			return Entry{}, err
		}
	} else {
		if err := r.Mkdir(rel, 0o755); err != nil {
			return Entry{}, err
		}
		if err := chownPath(r, rel, chown); err != nil {
			return Entry{}, err
		}
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return Entry{}, err
	}
	return EntryFrom(rel, fi), nil
}

// MkdirAllRoot creates rel and its ancestors inside r, one component at a
// time.
func MkdirAllRoot(r *os.Root, rel string, chown func(*os.File) error) error {
	if rel == "." || rel == "" {
		return nil
	}
	cur := ""
	for _, part := range splitRel(rel) {
		cur = filepath.Join(cur, part)
		err := r.Mkdir(cur, 0o755)
		switch {
		case err == nil:
			if err := chownPath(r, cur, chown); err != nil {
				return err
			}
		case errors.Is(err, fs.ErrExist):
			fi, serr := r.Stat(cur)
			if serr != nil {
				return serr
			}
			if !fi.IsDir() {
				return fmt.Errorf("%w: %s", ErrNotDir, filepath.ToSlash(cur))
			}
		default:
			return err
		}
	}
	return nil
}

func chownPath(r *os.Root, rel string, chown func(*os.File) error) error {
	if chown == nil {
		return nil
	}
	f, err := r.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	return chown(f)
}

// Rename implements FS.
func (l *Local) Rename(_ context.Context, tenantID string, root Root, from, to string, overwrite bool) (Entry, error) {
	toRel, err := CleanRel(to)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = l.withRoot(tenantID, root, from, func(r *os.Root, fromRel string) error {
		var err error
		e, err = RenameRoot(r, fromRel, toRel, overwrite)
		return err
	})
	return e, err
}

// RenameRoot moves fromRel to toRel inside r (shared with the guest file
// service).
func RenameRoot(r *os.Root, fromRel, toRel string, overwrite bool) (Entry, error) {
	if fromRel == "." || toRel == "." {
		return Entry{}, ErrIsRoot
	}
	fi, err := r.Lstat(fromRel)
	if err != nil {
		return Entry{}, err
	}
	if fromRel == toRel {
		return EntryFrom(toRel, fi), nil
	}
	if strings.HasPrefix(toRel+string(filepath.Separator), fromRel+string(filepath.Separator)) {
		return Entry{}, fmt.Errorf("%w: cannot move a directory into itself", ErrInvalidPath)
	}
	if dst, err := r.Lstat(toRel); err == nil {
		if !overwrite {
			return Entry{}, fmt.Errorf("%w: %s", fs.ErrExist, filepath.ToSlash(toRel))
		}
		if dst.IsDir() {
			return Entry{}, fmt.Errorf("%w: destination is a directory", ErrIsDir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Entry{}, err
	}
	if err := renameIn(r, fromRel, r, toRel); err != nil {
		return Entry{}, err
	}
	fi, err = r.Lstat(toRel)
	if err != nil {
		return Entry{}, err
	}
	return EntryFrom(toRel, fi), nil
}

// Remove implements FS.
func (l *Local) Remove(_ context.Context, tenantID string, root Root, p string, recursive bool) error {
	return l.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		return RemoveRoot(r, rel, recursive)
	})
}

// RemoveRoot removes rel inside r (shared with the guest file service).
func RemoveRoot(r *os.Root, rel string, recursive bool) error {
	if rel == "." {
		return ErrIsRoot
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if fi.IsDir() && recursive {
		return removeTree(r, rel)
	}
	return r.Remove(rel)
}

func removeTree(r *os.Root, rel string) error {
	fi, err := r.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		d, err := r.Open(rel)
		if err != nil {
			return err
		}
		names, err := d.Readdirnames(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := removeTree(r, filepath.Join(rel, n)); err != nil {
				return err
			}
		}
	}
	if err := r.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
