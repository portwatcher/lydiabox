//go:build linux

package acp

import (
	"os"

	"golang.org/x/sys/unix"
)

// MakeRaw puts the terminal of f in raw mode (no echo, no line editing, no
// output translation), so JSON lines pass unchanged. It does nothing when f
// is not a terminal. The returned func restores the old mode.
func MakeRaw(f *os.File) (restore func()) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	t := *old
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if unix.IoctlSetTermios(fd, unix.TCSETS, &t) != nil {
		return func() {}
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }
}
