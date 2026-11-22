package rpc

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// chmodSocket sets the mode of the socket file at path. It does not follow
// a symlink: it opens the path itself (O_PATH|O_NOFOLLOW), requires a
// socket, and changes the mode of that inode through /proc.
func chmodSocket(path string, mode os.FileMode) error {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return &os.PathError{Op: "stat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("rpc: %s is not a socket", path)
	}
	return os.Chmod("/proc/self/fd/"+strconv.Itoa(fd), mode)
}
