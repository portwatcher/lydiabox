package agentfs

import (
	"errors"
	"fmt"
	"io/fs"
)

// Error codes that carry FS errors across the guest RPC (rpc.Error.Code).
const (
	CodeInvalidPath = "fs_invalid_path"
	CodeEscapes     = "fs_escapes"
	CodeUnknownRoot = "fs_unknown_root"
	CodeIsRoot      = "fs_is_root"
	CodeNotDir      = "fs_not_dir"
	CodeIsDir       = "fs_is_dir"
	CodeNotRegular  = "fs_not_regular"
	CodeTooLarge    = "fs_too_large"
	CodeConflict    = "fs_conflict"
	CodeNotExist    = "fs_not_exist"
	CodeExist       = "fs_exist"
	CodePermission  = "fs_permission"
)

var codeErrs = []struct {
	code string
	err  error
}{
	{CodeInvalidPath, ErrInvalidPath},
	{CodeEscapes, ErrEscapes},
	{CodeUnknownRoot, ErrUnknownRoot},
	{CodeIsRoot, ErrIsRoot},
	{CodeNotDir, ErrNotDir},
	{CodeIsDir, ErrIsDir},
	{CodeNotRegular, ErrNotRegular},
	{CodeTooLarge, ErrTooLarge},
	{CodeConflict, ErrConflict},
	{CodeNotExist, fs.ErrNotExist},
	{CodeExist, fs.ErrExist},
	{CodePermission, fs.ErrPermission},
}

// Code returns the RPC error code of an FS error, or "" when err is not one.
func Code(err error) string {
	for _, c := range codeErrs {
		if errors.Is(err, c.err) {
			return c.code
		}
	}
	return ""
}

// FromCode rebuilds an FS error from an RPC error code and message. It
// returns nil for a code that is not an FS code.
func FromCode(code, msg string) error {
	for _, c := range codeErrs {
		if c.code == code {
			return &codeError{base: c.err, msg: msg}
		}
	}
	return nil
}

// codeError keeps the guest message and matches the sentinel with errors.Is.
type codeError struct {
	base error
	msg  string
}

func (e *codeError) Error() string {
	if e.msg == "" {
		return e.base.Error()
	}
	return e.msg
}

func (e *codeError) Unwrap() error { return e.base }

// Unavailable wraps a failure to reach the guest as ErrUnavailable.
func Unavailable(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, fmt.Sprintf(format, args...))
}
