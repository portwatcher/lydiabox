// Package files implements the File Manager (docs/api.md, "/api/v1/fs")
// over an agentfs.FS: the host JuiceFS mount in juicefs storage mode
// (agentfs.Local, an os.Root sandbox) or the guest daemon file methods in
// provider storage mode (remotefs). The sandbox rules (CleanRel, os.Root
// lookups that reject "..", absolute paths, and escaping symlinks) live in
// internal/agentfs and internal/guest/guestfs.
package files

import (
	"errors"

	"github.com/lydiabox/lydiabox/internal/agentfs"
)

// Root names exposed by the API.
const (
	RootWorkspace = agentfs.RootWorkspace
	RootHome      = agentfs.RootHome
)

// RootNames lists the roots in display order.
var RootNames = agentfs.RootNames

// Errors returned by Service operations; http.go maps them to status codes.
var (
	ErrInvalidPath = agentfs.ErrInvalidPath
	ErrEscapes     = agentfs.ErrEscapes
	ErrUnknownRoot = agentfs.ErrUnknownRoot
	ErrIsRoot      = agentfs.ErrIsRoot
	ErrNotDir      = agentfs.ErrNotDir
	ErrIsDir       = agentfs.ErrIsDir
	ErrNotRegular  = agentfs.ErrNotRegular
	ErrTooLarge    = agentfs.ErrTooLarge
	ErrConflict    = agentfs.ErrConflict
	ErrUnavailable = agentfs.ErrUnavailable
)

// ErrNotText is returned when a text read finds binary content.
var ErrNotText = errors.New("file is not UTF-8 text")

// CleanRel validates a client path (see agentfs.CleanRel).
func CleanRel(p string) (string, error) { return agentfs.CleanRel(p) }

// Entry describes one file or directory.
type Entry = agentfs.Entry
