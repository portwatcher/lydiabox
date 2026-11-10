package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Mkdir creates a directory. With parents, missing ancestors are created and
// an existing directory is not an error.
func (s *Service) Mkdir(tenantID, root, p string, parents bool) (Entry, error) {
	var e Entry
	err := s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		if rel == "." {
			return ErrIsRoot
		}
		if parents {
			if err := s.mkdirAll(r, rel); err != nil {
				return err
			}
		} else {
			if err := r.Mkdir(rel, 0o755); err != nil {
				return err
			}
			if err := s.ownPath(r, rel); err != nil {
				return err
			}
		}
		fi, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		e = entryFrom(rel, fi)
		return nil
	})
	return e, err
}

// mkdirAll creates rel and its ancestors inside r, one component at a time.
func (s *Service) mkdirAll(r *os.Root, rel string) error {
	if rel == "." {
		return nil
	}
	cur := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		err := r.Mkdir(cur, 0o755)
		switch {
		case err == nil:
			if err := s.ownPath(r, cur); err != nil {
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

func (s *Service) ownPath(r *os.Root, rel string) error {
	if s.opts.Owner == nil {
		return nil
	}
	f, err := r.Open(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	return s.own(f)
}

// Rename moves from to to inside one root. Without overwrite an existing
// destination is an error (fs.ErrExist).
func (s *Service) Rename(tenantID, root, from, to string, overwrite bool) (Entry, error) {
	var e Entry
	toRel, err := CleanRel(to)
	if err != nil {
		return e, err
	}
	err = s.withRoot(tenantID, root, from, func(r *os.Root, fromRel string) error {
		if fromRel == "." || toRel == "." {
			return ErrIsRoot
		}
		if _, err := r.Lstat(fromRel); err != nil {
			return err
		}
		if fromRel == toRel {
			return nil
		}
		if strings.HasPrefix(toRel+string(filepath.Separator), fromRel+string(filepath.Separator)) {
			return fmt.Errorf("%w: cannot move a directory into itself", ErrInvalidPath)
		}
		if dst, err := r.Lstat(toRel); err == nil {
			if !overwrite {
				return fmt.Errorf("%w: %s", fs.ErrExist, filepath.ToSlash(toRel))
			}
			if dst.IsDir() {
				return fmt.Errorf("%w: destination is a directory", ErrIsDir)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := renameIn(r, fromRel, r, toRel); err != nil {
			return err
		}
		fi, err := r.Lstat(toRel)
		if err != nil {
			return err
		}
		e = entryFrom(toRel, fi)
		return nil
	})
	return e, err
}

// Delete removes a file, symlink, or empty directory; with recursive it
// removes a directory tree. Symlinks are removed, never followed.
func (s *Service) Delete(tenantID, root, p string, recursive bool) error {
	return s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
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
	})
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

// UploadResult describes one uploaded file.
type UploadResult struct {
	Entry
	Replaced bool `json:"replaced"`
}

// Upload writes src to dir/relPath, creating intermediate directories (used
// by folder drag-and-drop). relPath is validated like any client path.
func (s *Service) Upload(tenantID, root, dir, relPath string, src io.Reader, overwrite bool, limit int64) (UploadResult, error) {
	var res UploadResult
	dirRel, err := CleanRel(dir)
	if err != nil {
		return res, err
	}
	fileRel, err := CleanRel(relPath)
	if err != nil {
		return res, err
	}
	if fileRel == "." {
		return res, fmt.Errorf("%w: upload needs a file name", ErrInvalidPath)
	}
	target := filepath.Join(dirRel, fileRel)
	err = s.withRoot(tenantID, root, filepath.ToSlash(target), func(r *os.Root, rel string) error {
		if err := s.mkdirAll(r, filepath.Dir(rel)); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if fi, err := r.Lstat(rel); err == nil {
			if fi.IsDir() {
				return ErrIsDir
			}
			if !overwrite {
				return fmt.Errorf("%w: %s", fs.ErrExist, filepath.ToSlash(rel))
			}
			res.Replaced = true
			if fi.Mode().IsRegular() {
				mode = fi.Mode().Perm()
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := s.writeAtomic(r, rel, src, mode, limit); err != nil {
			return err
		}
		fi, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		res.Entry = entryFrom(rel, fi)
		return nil
	})
	return res, err
}
