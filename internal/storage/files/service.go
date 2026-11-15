package files

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lydiabox/lydiabox/internal/agentfs"
)

// Dirs resolves the host directories of a tenant. storage.Provider implements it.
type Dirs = agentfs.Dirs

// Options configures a Service.
type Options struct {
	// MaxTextBytes bounds text reads and saves (default 8 MiB).
	MaxTextBytes int64
}

// Service performs sandboxed file operations under the tenant roots.
type Service struct {
	fs   agentfs.FS
	opts Options
}

// NewService builds a Service over an agentfs.FS.
func NewService(fsys agentfs.FS, opts Options) *Service {
	if opts.MaxTextBytes <= 0 {
		opts.MaxTextBytes = 8 << 20
	}
	return &Service{fs: fsys, opts: opts}
}

// NewLocalService builds a Service over the host directories of dirs
// (juicefs storage mode).
func NewLocalService(dirs Dirs, opts Options) *Service {
	return NewService(agentfs.NewLocal(dirs, agentfs.LocalOptions{}), opts)
}

// FS returns the underlying filesystem.
func (s *Service) FS() agentfs.FS { return s.fs }

// Roots lists the roots and their guest paths.
func (s *Service) Roots(ctx context.Context, tenantID string) []agentfs.RootInfo {
	return s.fs.Roots(ctx, tenantID)
}

// Stat describes one path without following a final symlink.
func (s *Service) Stat(ctx context.Context, tenantID, root, p string) (Entry, error) {
	return s.fs.Lstat(ctx, tenantID, root, p)
}

// List returns the entries of a directory, directories first, then by name.
func (s *Service) List(ctx context.Context, tenantID, root, p string) (Entry, []Entry, error) {
	self, entries, err := s.fs.List(ctx, tenantID, root, p)
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
func (s *Service) ReadText(ctx context.Context, tenantID, root, p string) (TextFile, error) {
	f, err := s.fs.Open(ctx, tenantID, root, p)
	if err != nil {
		return TextFile{}, err
	}
	defer f.Close()
	info := f.Info()
	if info.Size > s.opts.MaxTextBytes {
		return TextFile{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrTooLarge, info.Size, s.opts.MaxTextBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, s.opts.MaxTextBytes+1))
	if err != nil {
		return TextFile{}, err
	}
	if int64(len(data)) > s.opts.MaxTextBytes {
		return TextFile{}, ErrTooLarge
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return TextFile{}, ErrNotText
	}
	return TextFile{Entry: info, Content: string(data)}, nil
}

// WriteText atomically replaces or creates a text file. The parent directory
// must exist. When baseModTime is non-nil and the file exists with a
// different modification time, ErrConflict is returned.
func (s *Service) WriteText(ctx context.Context, tenantID, root, p, content string, baseModTime *time.Time) (Entry, error) {
	if int64(len(content)) > s.opts.MaxTextBytes {
		return Entry{}, ErrTooLarge
	}
	res, err := s.fs.Write(ctx, tenantID, root, p, strings.NewReader(content), agentfs.WriteOptions{
		Overwrite: true, IfModTime: baseModTime, Limit: -1, RegularOnly: true,
	})
	return res.Entry, err
}

// OpenFile opens a regular file for streaming download. The caller closes it.
func (s *Service) OpenFile(ctx context.Context, tenantID, root, p string) (agentfs.File, error) {
	return s.fs.Open(ctx, tenantID, root, p)
}

// Mkdir creates a directory. With parents, missing ancestors are created and
// an existing directory is not an error.
func (s *Service) Mkdir(ctx context.Context, tenantID, root, p string, parents bool) (Entry, error) {
	return s.fs.Mkdir(ctx, tenantID, root, p, parents)
}

// Rename moves from to to inside one root. Without overwrite an existing
// destination is an error (fs.ErrExist).
func (s *Service) Rename(ctx context.Context, tenantID, root, from, to string, overwrite bool) (Entry, error) {
	return s.fs.Rename(ctx, tenantID, root, from, to, overwrite)
}

// Delete removes a file, symlink, or empty directory; with recursive it
// removes a directory tree. Symlinks are removed, never followed.
func (s *Service) Delete(ctx context.Context, tenantID, root, p string, recursive bool) error {
	return s.fs.Remove(ctx, tenantID, root, p, recursive)
}

// UploadResult describes one uploaded file.
type UploadResult = agentfs.WriteResult

// Upload writes src to dir/relPath, creating intermediate directories (used
// by folder drag-and-drop). relPath is validated like any client path.
func (s *Service) Upload(ctx context.Context, tenantID, root, dir, relPath string, src io.Reader, overwrite bool, limit int64) (UploadResult, error) {
	dirRel, err := CleanRel(dir)
	if err != nil {
		return UploadResult{}, err
	}
	fileRel, err := CleanRel(relPath)
	if err != nil {
		return UploadResult{}, err
	}
	if fileRel == "." {
		return UploadResult{}, fmt.Errorf("%w: upload needs a file name", ErrInvalidPath)
	}
	target := filepath.ToSlash(filepath.Join(dirRel, fileRel))
	return s.fs.Write(ctx, tenantID, root, target, src, agentfs.WriteOptions{
		Overwrite: overwrite, MkdirParents: true, Limit: limit,
	})
}
