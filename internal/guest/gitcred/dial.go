package gitcred

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// HostAddrEnv names the environment variable that carries the host broker
// address inside the guest: "vsock://2:9001" (Cloud Hypervisor, host CID 2)
// or "unix:///run/lydiabox/host-9001.sock" (docker driver).
const HostAddrEnv = "LYDIABOX_HOST_ADDR"

// ErrNoHostAddr is returned when LYDIABOX_HOST_ADDR is not set.
var ErrNoHostAddr = errors.New(HostAddrEnv + " is not set; run inside a LydiaBox guest")

// Dial connects to the host broker address (vsock://<cid>:<port> or
// unix://<path>).
func Dial(ctx context.Context, addr string) (net.Conn, error) {
	if addr == "" {
		return nil, ErrNoHostAddr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", HostAddrEnv, addr, err)
	}
	switch u.Scheme {
	case "unix":
		p := u.Path
		if p == "" {
			p = u.Opaque
		}
		if p == "" {
			return nil, fmt.Errorf("invalid %s %q: missing socket path", HostAddrEnv, addr)
		}
		var d net.Dialer
		return d.DialContext(ctx, "unix", p)
	case "vsock":
		cid, err := strconv.ParseUint(u.Hostname(), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: bad CID", HostAddrEnv, addr)
		}
		port, err := strconv.ParseUint(u.Port(), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: bad port", HostAddrEnv, addr)
		}
		return dialVsock(ctx, uint32(cid), uint32(port))
	}
	return nil, fmt.Errorf("invalid %s %q: scheme must be vsock or unix", HostAddrEnv, addr)
}
