//go:build unix && !linux

package rpc

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// chmodSocket sets the mode of the socket file at path without following a
// symlink, and requires a socket.
func chmodSocket(path string, mode os.FileMode) error {
	if err := unix.Fchmodat(unix.AT_FDCWD, path, uint32(mode.Perm()), unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "chmod", Path: path, Err: err}
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("rpc: %s is not a socket", path)
	}
	return nil
}
