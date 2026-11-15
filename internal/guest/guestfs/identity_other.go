//go:build unix && !linux

package guestfs

import "errors"

const canSwitchIdentity = false

func runAs(*Identity, func() error) error {
	return errors.New("guestfs: switching the file system identity is supported only on Linux")
}
