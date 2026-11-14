package cloudflare

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
)

var esc = url.PathEscape

// Tunnel is a Cloudflare Tunnel (cfd_tunnel).
type Tunnel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ConfigSrc string `json:"config_src,omitempty"`
	DeletedAt string `json:"deleted_at,omitempty"`
}

// FindTunnel returns the tunnel called name that is not deleted, or nil.
func (c *Client) FindTunnel(ctx context.Context, name string) (*Tunnel, error) {
	q := url.Values{"name": {name}, "is_deleted": {"false"}}
	tunnels, err := list[Tunnel](ctx, c, c.accountPath("/cfd_tunnel"), q)
	if err != nil {
		return nil, err
	}
	for _, t := range tunnels {
		if t.Name == name && t.DeletedAt == "" {
			return &t, nil
		}
	}
	return nil, nil
}

// CreateTunnel creates a remotely managed tunnel: its ingress rules live in
// Cloudflare (PutTunnelConfig), so cloudflared needs only the token.
func (c *Client) CreateTunnel(ctx context.Context, name string) (Tunnel, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Tunnel{}, err
	}
	body := map[string]string{
		"name":          name,
		"config_src":    "cloudflare",
		"tunnel_secret": base64.StdEncoding.EncodeToString(secret),
	}
	var t Tunnel
	_, err := c.do(ctx, http.MethodPost, c.accountPath("/cfd_tunnel"), nil, body, &t)
	return t, err
}

// TunnelToken returns the token that `cloudflared tunnel run` needs.
func (c *Client) TunnelToken(ctx context.Context, id string) (string, error) {
	var token string
	_, err := c.do(ctx, http.MethodGet, c.accountPath("/cfd_tunnel/%s/token", esc(id)), nil, nil, &token)
	return token, err
}

// IngressRule is one ingress rule of a remotely managed tunnel.
type IngressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Path     string `json:"path,omitempty"`
	Service  string `json:"service"`
}

// TunnelConfig is the remote configuration of a tunnel.
type TunnelConfig struct {
	Ingress []IngressRule `json:"ingress"`
}

// PutTunnelConfig replaces the ingress rules of a tunnel: hostname goes to
// service, and every other request gets a 404 from cloudflared.
func (c *Client) PutTunnelConfig(ctx context.Context, id, hostname, service string) error {
	body := map[string]TunnelConfig{"config": {Ingress: []IngressRule{
		{Hostname: hostname, Service: service},
		{Service: "http_status:404"},
	}}}
	_, err := c.do(ctx, http.MethodPut, c.accountPath("/cfd_tunnel/%s/configurations", esc(id)), nil, body, nil)
	return err
}

// DeleteTunnel removes the stale connections of a tunnel, then the tunnel.
// A running cloudflared reconnects and blocks the delete, so stop LydiaBox
// first.
func (c *Client) DeleteTunnel(ctx context.Context, id string) error {
	if _, err := c.do(ctx, http.MethodDelete, c.accountPath("/cfd_tunnel/%s/connections", esc(id)), nil, nil, nil); err != nil && !IsNotFound(err) {
		return err
	}
	_, err := c.do(ctx, http.MethodDelete, c.accountPath("/cfd_tunnel/%s", esc(id)), nil, nil, nil)
	return err
}

// TunnelTarget is the DNS CNAME target of a tunnel.
func TunnelTarget(id string) string { return id + ".cfargotunnel.com" }
