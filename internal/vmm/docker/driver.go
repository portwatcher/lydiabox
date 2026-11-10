// Package docker is the container fallback VMM driver for hosts without
// /dev/kvm. It runs the same desktop root filesystem image with the guest
// daemon listening on a UNIX socket in a bind-mounted directory, so the host
// speaks the same RPC protocol as with Cloud Hypervisor's vsock.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// DefaultImage is the desktop image built by deploy/rootfs/build-artifacts.sh
// (--docker-image).
const DefaultImage = "lydiabox/guest:latest"

// Options configures the driver.
type Options struct {
	// RunDir holds per-tenant socket directories ($DATA_DIR/run).
	RunDir string
	// Docker is the docker CLI (default "docker").
	Docker string
	// Image is used when the spec does not name one (default DefaultImage).
	Image  string
	Runner storage.Runner
	// PublishIP is the host address the WebRTC UDP port is published on
	// (default 0.0.0.0).
	PublishIP string
	Logger    *slog.Logger
}

// Driver implements vmm.Driver with Docker containers.
type Driver struct{ opts Options }

// New returns a driver.
func New(opts Options) (*Driver, error) {
	if opts.RunDir == "" || opts.Runner == nil {
		return nil, errors.New("docker: RunDir and Runner are required")
	}
	if opts.Docker == "" {
		opts.Docker = "docker"
	}
	if opts.Image == "" {
		opts.Image = DefaultImage
	}
	if opts.PublishIP == "" {
		opts.PublishIP = "0.0.0.0"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Driver{opts: opts}, nil
}

// Name implements vmm.Driver.
func (d *Driver) Name() string { return "docker" }

// ContainerName returns the container of a tenant.
func ContainerName(tid string) string { return "lydiabox-" + tid }

// SocketDir returns the host directory mounted at /run/lydiabox.
func (d *Driver) SocketDir(tid string) (string, error) {
	if err := vmm.ValidTenantID(tid); err != nil {
		return "", err
	}
	return filepath.Join(d.opts.RunDir, "vm", tid, "sock"), nil
}

func (d *Driver) docker(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	err := d.opts.Runner.Run(ctx, storage.Command{Path: d.opts.Docker, Args: args, Stdout: &out})
	return strings.TrimSpace(out.String()), err
}

// CreateArgs returns the `docker create` arguments for spec.
func (d *Driver) CreateArgs(tid string, spec vmm.Spec, sockDir string) []string {
	image := spec.Image
	if image == "" {
		image = d.opts.Image
	}
	env := []string{
		rpc.EnvGuestListen + "=" + rpc.GuestListenDocker,
		rpc.EnvHostAddr + "=" + rpc.HostAddrDocker,
		rpc.EnvGatewayAddr + "=" + rpc.GatewayAddrDocker,
		rpc.EnvWebRTCPort + "=" + strconv.Itoa(spec.WebRTCPort),
		// Chrome's sandbox needs user namespaces an unprivileged container lacks.
		"LYDIABOX_CHROME_FLAGS=--no-sandbox",
	}
	args := []string{"create", "--name", ContainerName(tid), "--hostname", "lydiabox",
		"--label", "lydiabox.tenant=" + tid, "--shm-size", "2g", "--add-host", "host.docker.internal:host-gateway"}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.MemMB > 0 {
		args = append(args, "--memory", strconv.Itoa(spec.MemMB)+"m")
	}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args,
		"--volume", sockDir+":"+rpc.DockerSocketDir,
		"--volume", spec.HomeDir+":/home/agent",
		"--volume", spec.WorkspaceDir+":/workspace",
	)
	if spec.GuestBinary != "" {
		args = append(args, "--volume", spec.GuestBinary+":/usr/local/bin/lydiabox-guest:ro")
	}
	if spec.WebRTCPort > 0 {
		p := strconv.Itoa(spec.WebRTCPort)
		args = append(args, "--publish", d.opts.PublishIP+":"+p+":"+p+"/udp")
	}
	return append(args, image)
}

// Create implements vmm.Driver.
func (d *Driver) Create(ctx context.Context, tid string, spec vmm.Spec) error {
	sockDir, err := d.SocketDir(tid)
	if err != nil {
		return err
	}
	if spec.HomeDir == "" || spec.WorkspaceDir == "" {
		return errors.New("docker: home and workspace directories are required")
	}
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return err
	}
	// The container user (uid 1000) creates guest.sock in this directory.
	if err := os.Chmod(sockDir, 0o777); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(sockDir, "guest.sock"))
	_, _ = d.docker(ctx, "rm", "--force", ContainerName(tid))
	if _, err := d.docker(ctx, d.CreateArgs(tid, spec, sockDir)...); err != nil {
		return fmt.Errorf("docker: create container: %w", err)
	}
	return nil
}

// Boot implements vmm.Driver.
func (d *Driver) Boot(ctx context.Context, tid string) error {
	_, err := d.docker(ctx, "start", ContainerName(tid))
	return err
}

// Pause implements vmm.Driver.
func (d *Driver) Pause(ctx context.Context, tid string) error {
	_, err := d.docker(ctx, "pause", ContainerName(tid))
	return err
}

// Resume implements vmm.Driver.
func (d *Driver) Resume(ctx context.Context, tid string) error {
	_, err := d.docker(ctx, "unpause", ContainerName(tid))
	return err
}

// Shutdown implements vmm.Driver: `docker stop -t 30` (or `docker kill`
// with force), then removes the container.
func (d *Driver) Shutdown(ctx context.Context, tid string, force bool) error {
	if err := vmm.ValidTenantID(tid); err != nil {
		return err
	}
	name := ContainerName(tid)
	st, err := d.State(ctx, tid)
	if err != nil {
		return err
	}
	if st == vmm.StatePaused {
		_, _ = d.docker(ctx, "unpause", name)
	}
	if st != vmm.StateStopped {
		if force {
			_, err = d.docker(ctx, "kill", name)
		} else {
			_, err = d.docker(ctx, "stop", "--time", "30", name)
		}
		if err != nil && !notFound(err) {
			return fmt.Errorf("docker: stop container: %w", err)
		}
	}
	if _, err := d.docker(ctx, "rm", "--force", name); err != nil && !notFound(err) {
		return fmt.Errorf("docker: remove container: %w", err)
	}
	return nil
}

func notFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such")
}

// State implements vmm.Driver.
func (d *Driver) State(ctx context.Context, tid string) (vmm.State, error) {
	if err := vmm.ValidTenantID(tid); err != nil {
		return vmm.StateStopped, err
	}
	out, err := d.docker(ctx, "inspect", "--format", "{{.State.Status}}", ContainerName(tid))
	if err != nil {
		if notFound(err) {
			return vmm.StateStopped, nil
		}
		return vmm.StateStopped, fmt.Errorf("docker: inspect: %w", err)
	}
	switch out {
	case "running":
		return vmm.StateRunning, nil
	case "paused":
		return vmm.StatePaused, nil
	case "restarting":
		return vmm.StateBooting, nil
	}
	return vmm.StateStopped, nil
}

func guestSocket(dir string, port uint32) string {
	if port == rpc.PortGuestRPC {
		return filepath.Join(dir, "guest.sock")
	}
	return filepath.Join(dir, fmt.Sprintf("guest-%d.sock", port))
}

// DialGuest implements vmm.Driver: port 9000 is guest.sock in the shared
// socket directory.
func (d *Driver) DialGuest(ctx context.Context, tid string, port uint32) (net.Conn, error) {
	dir, err := d.SocketDir(tid)
	if err != nil {
		return nil, err
	}
	var dl net.Dialer
	return dl.DialContext(ctx, "unix", guestSocket(dir, port))
}

// ListenGuestPort implements vmm.Driver: host-<port>.sock in the shared
// socket directory (unix:///run/lydiabox/host-<port>.sock in the guest).
func (d *Driver) ListenGuestPort(_ context.Context, tid string, port uint32) (net.Listener, error) {
	dir, err := d.SocketDir(tid)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		return nil, err
	}
	return rpc.ListenUnix(filepath.Join(dir, fmt.Sprintf("host-%d.sock", port)), 0o666)
}
