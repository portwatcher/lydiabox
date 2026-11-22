//go:build !unix

package rpc

import (
	"fmt"
	"os"
)

// chmodSocket sets the mode of the socket file at path.
func chmodSocket(path string, mode os.FileMode) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("rpc: %s is not a socket", path)
	}
	return os.Chmod(path, mode)
}
