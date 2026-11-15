// Package agentfs is the filesystem seam between host code and the two
// persistent guest trees (/home/agent and /workspace). Host consumers (the
// File API, the harness config wirers, the connector file tools, and the
// harness workspace setup) use FS instead of host paths, so the same code
// runs in both storage modes:
//
//   - juicefs: Local operates on the host JuiceFS mount with an os.Root
//     sandbox (the trees are shared into the VM with virtio-fs).
//   - provider: remotefs.FS calls the guest daemon file methods (the trees
//     are on the machine disk, which the host cannot mount).
//
// Paths are slash-separated and relative to the root. CleanRel rejects
// absolute paths, ".." segments, backslashes, and NUL bytes before any
// filesystem access; implementations also refuse symlinks that leave the
// root.
package agentfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Root names a guest tree.
type Root = string

// Root names exposed by the API.
const (
	RootWorkspace Root = "workspace"
	RootHome      Root = "home"
)

// RootNames lists the roots in display order.
var RootNames = []Root{RootWorkspace, RootHome}

// Default guest paths of the roots.
const (
	DefaultGuestHome      = "/home/agent"
	DefaultGuestWorkspace = "/workspace"
)

// Errors returned by FS operations; the File API maps them to status codes
// and the guest RPC carries them as error codes (Code, FromCode).
var (
	ErrInvalidPath = errors.New("invalid path")
	ErrEscapes     = errors.New("path escapes the root")
	ErrUnknownRoot = errors.New("unknown root")
	ErrIsRoot      = errors.New("operation is not allowed on the root directory")
	ErrNotDir      = errors.New("not a directory")
	ErrIsDir       = errors.New("is a directory")
	ErrNotRegular  = errors.New("not a regular file")
	ErrTooLarge    = errors.New("file is too large")
	ErrConflict    = errors.New("file changed since it was read")
	// ErrUnavailable means the guest that holds the files cannot be reached
	// (provider storage mode: the machine did not wake in time).
	ErrUnavailable = errors.New("the agent machine that holds the files is not available")
)

// Entry describes one file or directory.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Type    string    `json:"type"` // "file", "dir", "symlink", or "other"
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

// EntryFrom builds an Entry from an OS-relative path and its FileInfo.
func EntryFrom(rel string, fi fs.FileInfo) Entry {
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
	return Entry{Name: name, Path: ToSlash(rel), Type: t, Size: fi.Size(),
		Mode: fmt.Sprintf("%04o", fi.Mode().Perm()), ModTime: fi.ModTime().UTC()}
}

// WriteOptions control an atomic write.
type WriteOptions struct {
	// Overwrite replaces an existing file; without it an existing file is
	// fs.ErrExist.
	Overwrite bool
	// MkdirParents creates missing parent directories.
	MkdirParents bool
	// IfModTime, when set, fails with ErrConflict when the existing file has
	// a different modification time.
	IfModTime *time.Time
	// Limit bounds the content size (< 0 disables the check).
	Limit int64
	// Mode is the mode of a new file (default 0644). An existing regular
	// file keeps its mode.
	Mode fs.FileMode
	// RegularOnly refuses to replace an existing entry that is not a
	// regular file (ErrNotRegular).
	RegularOnly bool
}

// WriteResult describes a written file.
type WriteResult struct {
	Entry
	Replaced bool `json:"replaced"`
}

// File is a regular file opened for streaming reads. Seek is supported so
// the File API can serve Range requests.
type File interface {
	io.ReadSeekCloser
	// Info describes the file as it was when it was opened.
	Info() Entry
}

// RootInfo names a root and its path inside the guest.
type RootInfo struct {
	Name      Root   `json:"name"`
	GuestPath string `json:"guestPath"`
}

// FS is the filesystem of the two guest trees of a tenant.
type FS interface {
	// Roots lists the roots and their guest paths. It does not wake the VM.
	Roots(ctx context.Context, tenantID string) []RootInfo

	// Lstat describes one path without following a final symlink.
	Lstat(ctx context.Context, tenantID string, root Root, rel string) (Entry, error)
	// List returns the directory entry and its children (unsorted).
	List(ctx context.Context, tenantID string, root Root, rel string) (Entry, []Entry, error)
	// Open opens a regular file for reading.
	Open(ctx context.Context, tenantID string, root Root, rel string) (File, error)
	// Write atomically replaces or creates a file with the content of src
	// (temporary file, fsync, rename).
	Write(ctx context.Context, tenantID string, root Root, rel string, src io.Reader, o WriteOptions) (WriteResult, error)
	// Mkdir creates a directory. With parents, missing ancestors are created
	// and an existing directory is not an error.
	Mkdir(ctx context.Context, tenantID string, root Root, rel string, parents bool) (Entry, error)
	// Rename moves from to to inside one root. Without overwrite an
	// existing destination is fs.ErrExist.
	Rename(ctx context.Context, tenantID string, root Root, from, to string, overwrite bool) (Entry, error)
	// Remove removes a file, symlink, or empty directory; with recursive a
	// directory tree. Symlinks are removed, never followed.
	Remove(ctx context.Context, tenantID string, root Root, rel string, recursive bool) error

	// ReadFile reads a whole file, following symlinks that stay inside the
	// root (config files of the harness wirers).
	ReadFile(ctx context.Context, tenantID string, root Root, rel string) ([]byte, error)
	// WriteFile atomically writes data and creates missing parents. An
	// existing file keeps its mode; a new file gets perm. A final symlink
	// that stays inside the root is written through.
	WriteFile(ctx context.Context, tenantID string, root Root, rel string, data []byte, perm fs.FileMode) error
	// MkdirAll creates a directory and its parents.
	MkdirAll(ctx context.Context, tenantID string, root Root, rel string) error
}

// CleanRel validates a client path and returns it in OS form relative to the
// root. "" and "." select the root itself. Absolute paths, ".." components,
// backslashes, and NUL bytes are rejected before any filesystem access.
func CleanRel(p string) (string, error) {
	if p == "" || p == "." {
		return ".", nil
	}
	if strings.ContainsRune(p, 0) || strings.Contains(p, `\`) {
		return "", fmt.Errorf("%w: %q contains a forbidden character", ErrInvalidPath, p)
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: %q must be relative to the root", ErrInvalidPath, p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: %q must not contain '..'", ErrInvalidPath, p)
		}
	}
	clean := path.Clean(p)
	if clean == "." {
		return ".", nil
	}
	rel := filepath.FromSlash(clean)
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, p)
	}
	return rel, nil
}

// ToSlash converts an OS-relative path to the API form ("" for the root).
func ToSlash(rel string) string {
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// CheckRoot validates a root name.
func CheckRoot(root Root) error {
	if root != RootHome && root != RootWorkspace {
		return fmt.Errorf("%w %q", ErrUnknownRoot, root)
	}
	return nil
}

// MapRootErr converts os.Root lookup failures into ErrEscapes. os.Root
// reports an escaping symlink or ".." traversal as a *PathError whose
// message is "path escapes from parent".
func MapRootErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return fmt.Errorf("%w: %s", ErrEscapes, pe.Path)
	}
	return err
}
