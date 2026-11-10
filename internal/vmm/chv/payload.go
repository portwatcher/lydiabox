package chv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"

	"github.com/lydiabox/lydiabox/internal/vmm"
)

// Cmdline is the guest kernel command line (docs/design.md section 4.3).
const Cmdline = "console=ttyS0 root=/dev/vda ro quiet"

// VMConfig is the vm.create payload.
type VMConfig struct {
	CPUs    CPUsConfig     `json:"cpus"`
	Memory  MemoryConfig   `json:"memory"`
	Payload PayloadConfig  `json:"payload"`
	Disks   []DiskConfig   `json:"disks"`
	FS      []FSConfig     `json:"fs"`
	Vsock   *VsockConfig   `json:"vsock"`
	Net     []NetConfig    `json:"net,omitempty"`
	Devices []DeviceConfig `json:"devices,omitempty"`
	Serial  *ConsoleConfig `json:"serial,omitempty"`
	Console *ConsoleConfig `json:"console,omitempty"`
	RNG     *RNGConfig     `json:"rng,omitempty"`
}

// CPUsConfig is the cpus section.
type CPUsConfig struct {
	BootVCPUs int `json:"boot_vcpus"`
	MaxVCPUs  int `json:"max_vcpus"`
}

// MemoryConfig is the memory section. Shared memory is required by the
// vhost-user virtio-fs devices.
type MemoryConfig struct {
	Size   int64 `json:"size"`
	Shared bool  `json:"shared"`
}

// PayloadConfig is the direct kernel boot payload.
type PayloadConfig struct {
	Kernel    string `json:"kernel"`
	Initramfs string `json:"initramfs"`
	Cmdline   string `json:"cmdline"`
}

// DiskConfig is one virtio-blk disk.
type DiskConfig struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

// FSConfig is one virtio-fs device.
type FSConfig struct {
	Tag       string `json:"tag"`
	Socket    string `json:"socket"`
	NumQueues int    `json:"num_queues"`
	QueueSize int    `json:"queue_size"`
}

// VsockConfig is the vsock device.
type VsockConfig struct {
	CID    uint32 `json:"cid"`
	Socket string `json:"socket"`
}

// NetConfig is a TAP network device. IP and Mask are the host side of the
// TAP; Cloud Hypervisor creates and configures the TAP when it is missing.
type NetConfig struct {
	Tap  string `json:"tap"`
	IP   string `json:"ip"`
	Mask string `json:"mask"`
	MAC  string `json:"mac"`
}

// DeviceConfig is a VFIO device.
type DeviceConfig struct {
	Path string `json:"path"`
}

// ConsoleConfig configures the serial port or console.
type ConsoleConfig struct {
	Mode string `json:"mode"`
	File string `json:"file,omitempty"`
}

// RNGConfig is the entropy device.
type RNGConfig struct {
	Src string `json:"src"`
}

// Paths are the per-tenant runtime files.
type Paths struct {
	Dir          string
	APISocket    string
	VFSHome      string
	VFSWorkspace string
	Vsock        string
	GuestImage   string
	SerialLog    string
	VMMLog       string
}

// PathsFor returns the runtime files of a tenant under runDir.
func PathsFor(runDir, tenantID string) Paths {
	dir := filepath.Join(runDir, "vm", tenantID)
	return Paths{
		Dir:          dir,
		APISocket:    filepath.Join(dir, "chv.sock"),
		VFSHome:      filepath.Join(dir, "vfs-home.sock"),
		VFSWorkspace: filepath.Join(dir, "vfs-workspace.sock"),
		Vsock:        filepath.Join(dir, "vsock.sock"),
		GuestImage:   filepath.Join(dir, "lydiabox-guest.ext4"),
		SerialLog:    filepath.Join(dir, "serial.log"),
		VMMLog:       filepath.Join(dir, "cloud-hypervisor.log"),
	}
}

// Network describes the guest network.
type Network struct {
	// Disabled leaves the guest without a NIC.
	Disabled bool
	Tap      string
	HostIP   string // 172.30.0.1
	GuestIP  string // 172.30.0.2
	Prefix   int    // 24
}

// DefaultNetwork returns the network of docs/design.md section 4.3 with a
// TAP name derived from the tenant.
func DefaultNetwork(tenantID string) Network {
	return Network{Tap: TapName(tenantID), HostIP: "172.30.0.1", GuestIP: "172.30.0.2", Prefix: 24}
}

// TapName returns a stable interface name of at most 15 characters.
func TapName(tenantID string) string {
	sum := sha256.Sum256([]byte(tenantID))
	return "lbx" + hex.EncodeToString(sum[:4])
}

// MAC returns a stable locally administered unicast MAC for the tenant.
func MAC(tenantID string) string {
	sum := sha256.Sum256([]byte("mac:" + tenantID))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", sum[0], sum[1], sum[2])
}

// Subnet returns the guest subnet in CIDR form.
func (n Network) Subnet() string {
	_, ipnet, err := net.ParseCIDR(fmt.Sprintf("%s/%d", n.GuestIP, n.Prefix))
	if err != nil {
		return n.GuestIP + "/32"
	}
	return ipnet.String()
}

func (n Network) mask() string {
	m := net.CIDRMask(n.Prefix, 32)
	return net.IP(m).String()
}

// BuildConfig returns the vm.create payload for spec.
func BuildConfig(spec vmm.Spec, p Paths, nw Network, tenantID string) (*VMConfig, error) {
	if spec.CPUs <= 0 || spec.MemMB <= 0 {
		return nil, fmt.Errorf("chv: invalid CPU (%d) or memory (%d MiB) size", spec.CPUs, spec.MemMB)
	}
	for name, v := range map[string]string{"kernel": spec.Kernel, "initramfs": spec.Initramfs, "rootfs": spec.Rootfs, "root-upper image": spec.RootUpper} {
		if v == "" {
			return nil, fmt.Errorf("chv: %s path is required", name)
		}
	}
	cfg := &VMConfig{
		CPUs:    CPUsConfig{BootVCPUs: spec.CPUs, MaxVCPUs: spec.CPUs},
		Memory:  MemoryConfig{Size: int64(spec.MemMB) << 20, Shared: true},
		Payload: PayloadConfig{Kernel: spec.Kernel, Initramfs: spec.Initramfs, Cmdline: Cmdline},
		Disks: []DiskConfig{
			{Path: spec.Rootfs, Readonly: true},
			{Path: p.GuestImage, Readonly: true},
			{Path: spec.RootUpper, Readonly: false},
		},
		FS: []FSConfig{
			{Tag: "lydia_home", Socket: p.VFSHome, NumQueues: 2, QueueSize: 1024},
			{Tag: "lydia_workspace", Socket: p.VFSWorkspace, NumQueues: 2, QueueSize: 1024},
		},
		Vsock:   &VsockConfig{CID: 3, Socket: p.Vsock},
		Serial:  &ConsoleConfig{Mode: "File", File: p.SerialLog},
		Console: &ConsoleConfig{Mode: "Off"},
		RNG:     &RNGConfig{Src: "/dev/urandom"},
	}
	if !nw.Disabled {
		cfg.Net = []NetConfig{{Tap: nw.Tap, IP: nw.HostIP, Mask: nw.mask(), MAC: MAC(tenantID)}}
	}
	if spec.GPUPCIAddr != "" {
		cfg.Devices = []DeviceConfig{{Path: "/sys/bus/pci/devices/" + spec.GPUPCIAddr}}
	}
	return cfg, nil
}
