//go:build !linux

package rpc

import (
	"context"
	"net"
)

func listenVsock(uint32, uint32) (net.Listener, error) { return nil, ErrVsockUnsupported }

func dialVsock(context.Context, uint32, uint32) (net.Conn, error) { return nil, ErrVsockUnsupported }
