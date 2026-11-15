//go:build unix

package agentfs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// renameIn renames fromRel in fromRoot to toRel in toRoot with renameat(2).
// Both parent directories are opened through os.Root, which rejects any
// lookup that leaves the root, and renameat never follows the final
// component, so a concurrent symlink swap cannot redirect the rename.
func renameIn(fromRoot *os.Root, fromRel string, toRoot *os.Root, toRel string) error {
	fromDir, err := fromRoot.Open(parentOf(fromRel))
	if err != nil {
		return err
	}
	defer fromDir.Close()
	toDir, err := toRoot.Open(parentOf(toRel))
	if err != nil {
		return err
	}
	defer toDir.Close()
	if err := unix.Renameat(int(fromDir.Fd()), filepath.Base(fromRel), int(toDir.Fd()), filepath.Base(toRel)); err != nil {
		return &os.LinkError{Op: "rename", Old: fromRel, New: toRel, Err: err}
	}
	return nil
}

func parentOf(rel string) string {
	d := filepath.Dir(rel)
	if d == "" {
		return "."
	}
	return d
}
