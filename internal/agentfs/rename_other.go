//go:build !unix

package agentfs

import (
	"os"
	"path/filepath"
)

// renameIn falls back to os.Rename on platforms without renameat(2). The
// parents are verified through os.Root first; LydiaBox hosts are Linux or
// macOS, so this path only keeps the package portable.
func renameIn(fromRoot *os.Root, fromRel string, toRoot *os.Root, toRel string) error {
	if _, err := fromRoot.Stat(filepath.Dir(fromRel)); err != nil {
		return err
	}
	if _, err := toRoot.Stat(filepath.Dir(toRel)); err != nil {
		return err
	}
	return os.Rename(filepath.Join(fromRoot.Name(), fromRel), filepath.Join(toRoot.Name(), toRel))
}
