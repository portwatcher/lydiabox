//go:build !unix

package storage

import "syscall"

func childProcAttr() *syscall.SysProcAttr { return nil }
