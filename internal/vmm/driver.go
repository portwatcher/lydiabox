// Package vmm runs the agent VM: the Driver abstraction implemented by the
// Cloud Hypervisor (chv) and Docker (docker) backends, and the Controller
// that boots the VM on demand, tracks activity leases, and hibernates it
// after LYDIABOX_IDLE_TIMEOUT without leases.
package vmm

import (
	"context"
	"fmt"
	"net"

	"github.com/lydiabox/lydiabox/internal/api"
)

// State is the VM power state reported by the API.
type State string

// VM states (docs/api.md section 3).
const (
	StateStopped State = api.VMStopped
	StateBooting State = api.VMBooting
	StateRunning State = api.VMRunning
	StatePaused  State = api.VMPaused
)

// Spec is everything a driver needs to create a tenant's VM.
type Spec struct {
	CPUs  int
	MemMB int
	// GPUPCIAddr is an optional VFIO PCI address (chv only).
	GPUPCIAddr string

	// Kernel, Initramfs, and Rootfs are the guest images (chv). The docker
	// driver runs Image instead.
	Kernel    string
	Initramfs string
	Rootfs    string
	Image     string
	// GuestBinary is the host path of the linux lydiabox-guest binary that
	// is injected into the guest (read-only), so upgrading lydiabox
	// upgrades the guest daemon without rebuilding the root filesystem.
	GuestBinary string
	// RootUpper is the persistent OverlayFS upper image (chv).
	RootUpper string
	// HomeDir and WorkspaceDir are the host directories shared as
	// /home/agent and /workspace.
	HomeDir      string
	WorkspaceDir string
	// WebRTCPort is the UDP port forwarded from the host to the guest for
	// desktop streaming (0 disables forwarding).
	WebRTCPort int
}

// Driver is a VM backend. Every method takes the tenant ID so one host
// can serve several tenants.
type Driver interface {
	// Name returns "chv" or "docker".
	Name() string
	// Create prepares the VM (processes, devices, container) without
	// starting the guest.
	Create(ctx context.Context, tenantID string, spec Spec) error
	// Boot starts a created VM.
	Boot(ctx context.Context, tenantID string) error
	// Pause freezes a running VM.
	Pause(ctx context.Context, tenantID string) error
	// Resume continues a paused VM.
	Resume(ctx context.Context, tenantID string) error
	// Shutdown stops the VM and releases its resources. force skips the
	// graceful phase.
	Shutdown(ctx context.Context, tenantID string, force bool) error
	// State reports the backend's view of the VM.
	State(ctx context.Context, tenantID string) (State, error)
	// DialGuest connects to a guest vsock port (host to guest).
	DialGuest(ctx context.Context, tenantID string, port uint32) (net.Conn, error)
	// ListenGuestPort accepts guest connections to a host vsock port
	// (guest to host). It can be called before the VM boots.
	ListenGuestPort(ctx context.Context, tenantID string, port uint32) (net.Listener, error)
}

// Status is the VMStatus object of docs/api.md section 7.
type Status = api.VMDetail

// ErrConflict is returned for an impossible power transition.
var ErrConflict = api.ErrVMConflict

// ErrUnavailable is returned when the VM or its guest cannot be reached.
var ErrUnavailable = api.ErrVMUnavailable

// ValidTenantID checks that a tenant ID is safe in file names, interface
// names, and container names: 1 to 64 letters, digits, '-', or '_'.
func ValidTenantID(id string) error {
	if id == "" || len(id) > 64 {
		return fmt.Errorf("vmm: invalid tenant ID %q", id)
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("vmm: invalid tenant ID %q", id)
		}
	}
	return nil
}
