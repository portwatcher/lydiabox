package storage

import (
	"os"
	"runtime"
)

// fuseHint explains a failed `juicefs mount` when the host has no FUSE
// support, the most common first-run problem. It returns "" when FUSE looks
// available.
func fuseHint(goos string, exists func(string) bool) string {
	switch goos {
	case "darwin":
		if !exists("/Library/Filesystems/macfuse.fs") {
			return " (macOS needs macFUSE for the JuiceFS mount: https://macfuse.github.io; or run LydiaBox in a Linux VM or container)"
		}
	case "linux":
		if !exists("/dev/fuse") {
			return " (/dev/fuse is missing: load the fuse kernel module, or give the container --device /dev/fuse --cap-add SYS_ADMIN)"
		}
	}
	return ""
}

func hostFUSEHint() string {
	return fuseHint(runtime.GOOS, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}
