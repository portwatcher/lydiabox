package files

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Dirs resolves the host directories of a tenant. storage.Provider implements it.
type Dirs interface {
	HomeDir(tenantID string) (string, error)
	WorkspaceDir(tenantID string) (string, error)
}

// Options configures a Service.
type Options struct {
	// MaxTextBytes bounds text reads and saves (default 8 MiB).
	MaxTextBytes int64
	// Owner, when set, receives ownership of created files and directories
	// (the guest user 1000:1000 when the host runs as root).
	Owner *Owner
}

// Owner is a uid/gid pair.
type Owner struct{ UID, GID int }

// Service performs sandboxed file operations under the tenant roots.
type Service struct {
	dirs Dirs
	opts Options
}

// NewService builds a Service. When opts.Owner is nil and the process runs as
// root, files are owned by the guest user 1000:1000.
func NewService(dirs Dirs, opts Options) *Service {
	if opts.MaxTextBytes <= 0 {
		opts.MaxTextBytes = 8 << 20
	}
	if opts.Owner == nil && os.Geteuid() == 0 {
		opts.Owner = &Owner{UID: 1000, GID: 1000}
	}
	return &Service{dirs: dirs, opts: opts}
}

// Entry describes one file or directory.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Type    string    `json:"type"` // "file", "dir", "symlink", or "other"
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

func entryFrom(rel string, fi fs.FileInfo) Entry {
	t := "other"
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		t = "symlink"
	case fi.IsDir():
		t = "dir"
	case fi.Mode().IsRegular():
		t = "file"
	}
	name := fi.Name()
	if rel == "." {
		name = ""
	}
	return Entry{Name: name, Path: toSlash(rel), Type: t, Size: fi.Size(),
		Mode: fmt.Sprintf("%04o", fi.Mode().Perm()), ModTime: fi.ModTime().UTC()}
}

// openRoot opens the os.Root of a named root. Opening per call keeps the
// handle valid across JuiceFS remounts.
func (s *Service) openRoot(tenantID, root string) (*os.Root, error) {
	var (
		dir string
		err error
	)
	switch root {
	case RootWorkspace:
		dir, err = s.dirs.WorkspaceDir(tenantID)
	case RootHome:
		dir, err = s.dirs.HomeDir(tenantID)
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownRoot, root)
	}
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open root %s: %w", root, err)
	}
	return r, nil
}

func (s *Service) withRoot(tenantID, root, p string, fn func(r *os.Root, rel string) error) error {
	rel, err := CleanRel(p)
	if err != nil {
		return err
	}
	r, err := s.openRoot(tenantID, root)
	if err != nil {
		return err
	}
	defer r.Close()
	return mapRootErr(fn(r, rel))
}

// Stat describes one path without following a final symlink.
func (s *Service) Stat(tenantID, root, p string) (Entry, error) {
	var e Entry
	err := s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		fi, err := r.Lstat(rel)
		if err != nil {
			return err
		}
		e = entryFrom(rel, fi)
		return nil
	})
	return e, err
}

// List returns the entries of a directory, directories first, then by name.
func (s *Service) List(tenantID, root, p string) (Entry, []Entry, error) {
	var (
		self    Entry
		entries []Entry
	)
	err := s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		f, err := r.Open(rel)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return ErrNotDir
		}
		self = entryFrom(rel, fi)
		des, err := f.ReadDir(-1)
		if err != nil {
			return err
		}
		entries = make([]Entry, 0, len(des))
		for _, de := range des {
			info, err := de.Info()
			if err != nil {
				continue // removed between ReadDir and Info
			}
			entries = append(entries, entryFrom(filepath.Join(rel, de.Name()), info))
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return self, entries, err
}

// TextFile is the content of a text file.
type TextFile struct {
	Entry
	Content string `json:"content"`
}

// ReadText returns a UTF-8 file of at most MaxTextBytes.
func (s *Service) ReadText(tenantID, root, p string) (TextFile, error) {
	var tf TextFile
	err := s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		f, err := r.Open(rel)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return ErrIsDir
		}
		if !fi.Mode().IsRegular() {
			return ErrNotRegular
		}
		if fi.Size() > s.opts.MaxTextBytes {
			return fmt.Errorf("%w: %d bytes exceeds %d", ErrTooLarge, fi.Size(), s.opts.MaxTextBytes)
		}
		data, err := io.ReadAll(io.LimitReader(f, s.opts.MaxTextBytes+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > s.opts.MaxTextBytes {
			return ErrTooLarge
		}
		if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return ErrNotText
		}
		tf = TextFile{Entry: entryFrom(rel, fi), Content: string(data)}
		return nil
	})
	return tf, err
}

// WriteText atomically replaces or creates a text file. The parent directory
// must exist. When baseModTime is non-nil and the file exists with a
// different modification time, ErrConflict is returned.
func (s *Service) WriteText(tenantID, root, p, content string, baseModTime *time.Time) (Entry, error) {
	var e Entry
	if int64(len(content)) > s.opts.MaxTextBytes {
		return e, ErrTooLarge
	}
	err := s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		if rel == "." {
			return ErrIsRoot
		}
		mode := fs.FileMode(0o644)
		if fi, err := r.Lstat(rel); err == nil {
			if fi.IsDir() {
				return ErrIsDir
			}
			if !fi.Mode().IsRegular() {
				return ErrNotRegular
			}
			if baseModTime != nil && !fi.ModTime().Equal(*baseModTime) {
				return ErrConflict
			}
			mode = fi.Mode().Perm()
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := s.writeAtomic(r, rel, strings.NewReader(content), mode, -1); err != nil {
			return err
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

// writeAtomic streams src into a temporary file in the target directory and
// renames it over rel. limit < 0 disables the size check.
func (s *Service) writeAtomic(r *os.Root, rel string, src io.Reader, mode fs.FileMode, limit int64) error {
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
	if err := s.own(f); err != nil {
		return err
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

func (s *Service) own(f *os.File) error {
	if s.opts.Owner == nil {
		return nil
	}
	return f.Chown(s.opts.Owner.UID, s.opts.Owner.GID)
}

// OpenFile opens a regular file for streaming download. The caller closes it.
func (s *Service) OpenFile(tenantID, root, p string) (*os.File, fs.FileInfo, error) {
	var (
		file *os.File
		info fs.FileInfo
	)
	err := s.withRoot(tenantID, root, p, func(r *os.Root, rel string) error {
		f, err := r.Open(rel)
		if err != nil {
			return err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if fi.IsDir() {
			f.Close()
			return ErrIsDir
		}
		if !fi.Mode().IsRegular() {
			f.Close()
			return ErrNotRegular
		}
		file, info = f, fi
		return nil
	})
	return file, info, err
}
