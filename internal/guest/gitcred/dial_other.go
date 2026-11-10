//go:build !linux

package gitcred

import (
	"context"
	"errors"
	"net"
)

func dialVsock(context.Context, uint32, uint32) (net.Conn, error) {
	return nil, errors.New("vsock is only available on Linux guests; use a unix:// host address")
}
