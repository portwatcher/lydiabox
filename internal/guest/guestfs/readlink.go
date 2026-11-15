//go:build unix

package guestfs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// readlinkIn reads the target of the symlink rel inside r. The parent
// directory is opened through the os.Root, so the lookup cannot leave it.
func readlinkIn(r *os.Root, rel string) (string, error) {
	parent := filepath.Dir(rel)
	d, err := r.Open(parent)
	if err != nil {
		return "", err
	}
	defer d.Close()
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(int(d.Fd()), filepath.Base(rel), buf)
	if err != nil {
		return "", &os.PathError{Op: "readlink", Path: rel, Err: err}
	}
	return string(buf[:n]), nil
}
