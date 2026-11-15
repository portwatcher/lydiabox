package instabox

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// Port numbers the API refuses to publish (desktop VNC, Orca).
const (
	ReservedDesktopPort = 5901
	ReservedOrcaPort    = 6768
)

func validPort(port int) error {
	if port < 1024 || port > 65535 || port == ReservedDesktopPort || port == ReservedOrcaPort {
		return fmt.Errorf("instabox: port %d cannot be published (1024-65535, not %d or %d)", port, ReservedDesktopPort, ReservedOrcaPort)
	}
	return nil
}

// PublishPort gives a port of a machine an HTTPS URL. Publishing a
// published port again keeps its URL. The call has no Idempotency-Key in
// the API; a retry is safe because a publish is a set operation, but a
// private port's token comes only in the answer that created or rotated
// it (see PublishPrivatePort).
func (c *Client) PublishPort(ctx context.Context, id string, req PublishPortRequest) (*PublicPort, error) {
	if err := validPort(req.Port); err != nil {
		return nil, err
	}
	var p PublicPort
	if err := c.call(ctx, request{method: http.MethodPost, path: machinePath(id, "ports"), jsonBody: req}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// PublishPrivatePort publishes port with private access and returns it
// with its token (ibp_...). When the port was already private, or a
// retried publish lost the answer that held the token, it rotates the
// token, which closes the connections that used the old one.
func (c *Client) PublishPrivatePort(ctx context.Context, id string, port int, name string) (*PublicPort, error) {
	p, err := c.PublishPort(ctx, id, PublishPortRequest{Port: port, Name: name, Access: AccessPrivate})
	if err != nil {
		return nil, err
	}
	if p.Token != "" {
		return p, nil
	}
	p, err = c.PublishPort(ctx, id, PublishPortRequest{Port: port, Name: name, Access: AccessPrivate, RotateToken: true})
	if err != nil {
		return nil, err
	}
	if p.Token == "" {
		return nil, fmt.Errorf("instabox: publish of private port %d returned no token", port)
	}
	return p, nil
}

// ListPorts returns the published ports of a machine.
func (c *Client) ListPorts(ctx context.Context, id string) (*PublicPortList, error) {
	var list PublicPortList
	if err := c.call(ctx, request{method: http.MethodGet, path: machinePath(id, "ports")}, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// UnpublishPort removes a port's URL and closes its connections.
func (c *Client) UnpublishPort(ctx context.Context, id string, port int) error {
	return c.call(ctx, request{method: http.MethodDelete, path: machinePath(id, "ports", strconv.Itoa(port))}, nil)
}
