//go:build !linux

package acp

import "os"

// MakeRaw does nothing outside Linux (the bridge runs in the Linux guest).
func MakeRaw(*os.File) (restore func()) { return func() {} }
