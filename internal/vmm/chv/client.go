// Package chv is the Cloud Hypervisor VMM driver: two virtiofsd daemons for
// /home/agent and /workspace, a cloud-hypervisor process driven through its
// REST API on a UNIX socket, a TAP network with NAT, and the hybrid vsock
// UNIX socket for host<->guest connections.
package chv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client is a Cloud Hypervisor REST API client over the API UNIX socket.
type Client struct {
	socket string
	http   *http.Client
}

// NewClient returns a client for the API socket at path.
func NewClient(path string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		DisableKeepAlives: true,
	}
	return &Client{socket: path, http: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

// APIError is a non-2xx API response.
type APIError struct {
	Endpoint string
	Status   int
	Body     string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("cloud-hypervisor %s: HTTP %d: %s", e.Endpoint, e.Status, strings.TrimSpace(e.Body))
}

func (c *Client) do(ctx context.Context, method, endpoint string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://localhost/api/v1/"+endpoint, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cloud-hypervisor %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return &APIError{Endpoint: endpoint, Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("cloud-hypervisor %s: decode response: %w", endpoint, err)
		}
	}
	return nil
}

// Ping calls GET vmm.ping.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "vmm.ping", nil, nil)
}

// CreateVM calls PUT vm.create.
func (c *Client) CreateVM(ctx context.Context, cfg *VMConfig) error {
	return c.do(ctx, http.MethodPut, "vm.create", cfg, nil)
}

// Boot calls PUT vm.boot.
func (c *Client) Boot(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.boot", nil, nil)
}

// Pause calls PUT vm.pause.
func (c *Client) Pause(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.pause", nil, nil)
}

// Resume calls PUT vm.resume.
func (c *Client) Resume(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.resume", nil, nil)
}

// ShutdownVM calls PUT vm.shutdown.
func (c *Client) ShutdownVM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vm.shutdown", nil, nil)
}

// ShutdownVMM calls PUT vmm.shutdown, which ends the cloud-hypervisor process.
func (c *Client) ShutdownVMM(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "vmm.shutdown", nil, nil)
}

// VMInfo is the subset of GET vm.info used by the driver.
type VMInfo struct {
	State string `json:"state"`
}

// Info calls GET vm.info. State is Created, Running, Paused, or Shutdown.
func (c *Client) Info(ctx context.Context) (VMInfo, error) {
	var info VMInfo
	err := c.do(ctx, http.MethodGet, "vm.info", nil, &info)
	return info, err
}
