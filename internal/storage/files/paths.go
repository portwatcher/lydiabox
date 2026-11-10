// Package files implements the offline-capable File Manager over the host
// JuiceFS mount (docs/api.md, "/api/v1/fs"). Every operation resolves paths
// through an os.Root opened on the selected root directory, so `..`,
// absolute paths, and symlinks that point outside the root are rejected by
// the kernel-level lookup, not only by string checks.
package files

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// Root names exposed by the API.
const (
	RootWorkspace = "workspace"
	RootHome      = "home"
)

// RootNames lists the roots in display order.
var RootNames = []string{RootWorkspace, RootHome}

// Errors returned by Service operations; http.go maps them to status codes.
var (
	ErrInvalidPath = errors.New("invalid path")
	ErrEscapes     = errors.New("path escapes the root")
	ErrUnknownRoot = errors.New("unknown root")
	ErrIsRoot      = errors.New("operation is not allowed on the root directory")
	ErrNotDir      = errors.New("not a directory")
	ErrIsDir       = errors.New("is a directory")
	ErrNotRegular  = errors.New("not a regular file")
	ErrTooLarge    = errors.New("file is too large")
	ErrNotText     = errors.New("file is not UTF-8 text")
	ErrConflict    = errors.New("file changed since it was read")
)

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

// toSlash converts an OS-relative path to the API form ("" for the root).
func toSlash(rel string) string {
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

// mapRootErr converts os.Root lookup failures into ErrEscapes. os.Root
// reports an escaping symlink or ".." traversal as a *PathError whose message
// is "path escapes from parent".
func mapRootErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *fs.PathError
	if errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "escapes") {
		return fmt.Errorf("%w: %s", ErrEscapes, pe.Path)
	}
	return err
}
