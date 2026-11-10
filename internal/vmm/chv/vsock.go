package chv

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// DialVsock connects to guest port through Cloud Hypervisor's hybrid vsock
// socket: it dials the UNIX socket, writes "CONNECT <port>\n", and expects
// "OK <host_port>\n" before the stream carries application data.
func DialVsock(ctx context.Context, socket string, port uint32) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("chv: dial vsock %s: %w", socket, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("chv: vsock CONNECT %d: %w", port, err)
	}
	line, err := readLine(conn, 64)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("chv: vsock CONNECT %d: %w", port, err)
	}
	if !strings.HasPrefix(line, "OK ") {
		conn.Close()
		return nil, fmt.Errorf("chv: vsock CONNECT %d refused: %q", port, line)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// readLine reads one '\n'-terminated line byte by byte, so no application
// bytes after the handshake are consumed.
func readLine(conn net.Conn, max int) (string, error) {
	buf := make([]byte, 0, max)
	var b [1]byte
	for len(buf) < max {
		n, err := conn.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				return string(buf), nil
			}
			buf = append(buf, b[0])
			continue
		}
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("handshake line longer than %d bytes", max)
}

// ListenerPath is the UNIX socket on which Cloud Hypervisor forwards guest
// connections to host port P: <vsock socket>_<P>.
func ListenerPath(vsockSocket string, port uint32) string {
	return fmt.Sprintf("%s_%d", vsockSocket, port)
}
