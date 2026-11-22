//go:build unix

package safefile

import "syscall"

// nonBlock makes open return at once for a FIFO, so the type check runs.
const nonBlock = syscall.O_NONBLOCK
