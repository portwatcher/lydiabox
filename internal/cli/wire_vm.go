package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/agentfs/remotefs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/bootstrap"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/guest/credproxy"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/vmm"
	"github.com/lydiabox/lydiabox/internal/vmm/chv"
	"github.com/lydiabox/lydiabox/internal/vmm/docker"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
)

// Phase 2 environment variables read by wireVM (documented in .env.example).
const (
	envGuestBinary     = "LYDIABOX_GUEST_BINARY"
	envDockerImage     = "LYDIABOX_DOCKER_IMAGE"
	envGatewayUpstream = "LYDIABOX_GATEWAY_UPSTREAM"
)

// vmRuntime is the VM subsystem shared with the later wiring functions.
type vmRuntime struct {
	Controller *vmm.Controller
	Driver     vmm.Driver
	// Gateway is the host side of the guest LLM gateway tunnel (vsock port
	// 9002). wireHarness installs the magpie gateway key with
	// Gateway.SetInjectHeaders.
	Gateway *credproxy.HostGateway
}

// wireVM builds the VMM driver (chv or docker), the guest client, the
// auto-wake controller, and the LLM gateway tunnel, and serves /api/v1/vm/*.
func wireVM(ctx context.Context, d *deps) (*component, error) {
	cfg := d.Config
	log := d.Logger.With("component", "vmm")
	if err := vmm.ValidTenantID(cfg.TenantID); err != nil {
		return nil, err
	}
	webrtcPort := webRTCPort(d)
	drv, err := newDriver(d, log)
	if err != nil {
		return nil, err
	}
	guest := guestclient.New(guestclient.Options{Dialer: drv, Logger: log})
	var holds vmm.HoldStore
	if d.Store != nil {
		// The hold of a user stop is in the state database, so a new host
		// process (a release, or the idle exit of a cell) keeps it.
		holds = d.Store
	}
	ctl, err := vmm.NewController(vmm.Options{
		Holds:       holds,
		Driver:      drv,
		Spec:        func(ctx context.Context, tid string) (vmm.Spec, error) { return vmSpec(ctx, d, tid, webrtcPort) },
		IdleTimeout: cfg.VM.IdleTimeout,
		Ready: func(ctx context.Context, tid string) error {
			_, err := guest.Ping(ctx, tid)
			return err
		},
		Flush: guest.Sync,
		OnStateChange: func(tid string, st vmm.State) {
			if st == vmm.StateStopped {
				guest.Reset(tid)
			}
		},
		CPUs: cfg.VM.CPUs, MemMB: cfg.VM.MemMB, GPU: cfg.VM.GPUPCIAddr, Logger: log,
	})
	if err != nil {
		return nil, err
	}
	guest.SetWaker(ctl)
	guest.SetLeaser(ctl)
	gw, err := credproxy.NewHostGateway(gatewayUpstream(cfg))
	if err != nil {
		return nil, err
	}
	d.VM = &vmRuntime{Controller: ctl, Driver: drv, Gateway: gw}
	d.Guest = guest
	d.VMStatus = ctl
	if cfg.ProviderStorage() {
		// The guest trees are on the machine disk: file requests take a
		// lease and wake the machine (remotefs).
		rfs, err := remotefs.New(remotefs.Options{Client: guest, Waker: filesWaker{ctl}, Leaser: ctl})
		if err != nil {
			return nil, err
		}
		d.Files = rfs
	}

	gwCtx, gwCancel := context.WithCancel(context.Background())
	tid := cfg.TenantID
	return &component{
		Name:   "vm",
		Routes: []api.RouteRegistrar{api.VMRoutes{Controller: ctl}},
		Start: func(ctx context.Context) error {
			if err := ctl.Refresh(ctx, tid); err != nil {
				log.Warn("read VM state", "err", err)
			}
			// Before the server serves requests: an open page must not wake
			// a VM that the user stopped before this process started.
			_ = ctl.LoadHold(ctx, tid)
			ln, err := drv.ListenGuestPort(ctx, tid, rpc.PortHostGateway)
			if err != nil {
				return fmt.Errorf("listen for the guest LLM gateway tunnel: %w", err)
			}
			go func() {
				if err := gw.Serve(gwCtx, ln); err != nil {
					log.Error("guest LLM gateway tunnel stopped", "err", err)
				}
			}()
			// The connector broker (built by wireConnector) is reached by the
			// guest's git-cred helper and connector MCP bridge on port 9001.
			if brk := connectorBroker(d); brk != nil {
				bln, err := drv.ListenGuestPort(ctx, tid, config.HostBrokerPort)
				if err != nil {
					return fmt.Errorf("listen for the guest connector broker: %w", err)
				}
				go func() {
					if err := brk.Serve(gwCtx, bln); err != nil {
						log.Error("guest connector broker stopped", "err", err)
					}
				}()
			}
			return nil
		},
		Stop: func(ctx context.Context) error {
			gwCancel()
			err := ctl.Close(ctx)
			if c, ok := drv.(io.Closer); ok {
				err = errors.Join(err, c.Close())
			}
			return err
		},
	}, nil
}

// defaultWebRTCPort is the UDP port forwarded when LYDIABOX_WEBRTC_UDP_PORT
// is not set: none when the stream relays through the stored TURN key.
func defaultWebRTCPort(d *deps) int {
	if d.StoredTURN {
		return 0
	}
	return rpc.DefaultWebRTCPort
}

// webRTCPort is the host UDP port forwarded to the guest: the configured
// one (config validates it), else the default.
func webRTCPort(d *deps) int {
	if d.Driver == config.DriverInstabox || (d.Config != nil && d.Config.CellMode && !d.Config.VM.WebRTCPortSet) {
		// No host-wide UDP port: a remote machine gets no forward, and a
		// cell shares the host with other cells.
		return 0
	}
	if d.Config != nil && d.Config.VM.WebRTCPortSet {
		return d.Config.VM.WebRTCPort
	}
	return defaultWebRTCPort(d)
}

// gatewayUpstream is the host magpie gateway that the guest LLM tunnel
// forwards to: LYDIABOX_GATEWAY_UPSTREAM, else the configured magpie
// address.
func gatewayUpstream(cfg *config.Config) string {
	if v := os.Getenv(envGatewayUpstream); v != "" {
		return v
	}
	return "http://" + cfg.Magpie.Addr
}

// guestNetwork returns the chv guest network of a tenant: the configured
// subnet index, TAP name, and nftables table, else the stored subnet index
// and the names derived from the tenant ID.
func guestNetwork(ctx context.Context, d *deps, tid string) (chv.Network, error) {
	n := d.Config.VM.SubnetIndex
	if n == config.SubnetAuto {
		var err error
		if n, err = d.Store.TenantSubnet(ctx, tid); err != nil {
			return chv.Network{}, err
		}
	}
	nw := chv.TenantNetwork(tid, n)
	if d.Config.VM.Tap != "" {
		nw.Tap = d.Config.VM.Tap
	}
	nw.Table = d.Config.VM.NftTable
	return nw, nil
}

func newDriver(d *deps, log interface{ Warn(string, ...any) }) (vmm.Driver, error) {
	cfg := d.Config
	runner := storage.OSRunner{}
	if d.Driver == config.DriverInstabox {
		return newInstaboxDriver(d)
	}
	if d.Driver == config.DriverCHV {
		resolve := func(name string) string {
			p, err := d.Bootstrap.Resolve(name)
			if err != nil {
				log.Warn("runtime binary missing; the VM cannot boot until it is installed", "binary", name, "err", err)
				return name
			}
			return p
		}
		return chv.New(chv.Options{
			RunDir: cfg.RunDir(), CloudHypervisor: resolve("cloud-hypervisor"), Virtiofsd: resolve("virtiofsd"),
			Runner: runner, Logger: d.Logger.With("component", "chv"),
			// "default" keeps 172.30.0.0/24; other tenants get a stable
			// 172.30.<n>.0/24 unless LYDIABOX_VM_SUBNET_INDEX sets it.
			Network: func(ctx context.Context, tid string) (chv.Network, error) { return guestNetwork(ctx, d, tid) },
		})
	}
	return docker.New(docker.Options{RunDir: cfg.RunDir(), Image: os.Getenv(envDockerImage), Runner: runner,
		Logger: d.Logger.With("component", "docker")})
}

// vmSpec assembles the VM spec at boot time, fetching guest images from
// LYDIABOX_ROOTFS_URL when they are missing (chv).
func vmSpec(ctx context.Context, d *deps, tid string, webrtcPort int) (vmm.Spec, error) {
	cfg := d.Config
	if d.Driver == config.DriverInstabox {
		// The guest image snapshot holds the guest daemon and the desktop.
		return vmm.Spec{CPUs: cfg.VM.CPUs, MemMB: cfg.VM.MemMB}, nil
	}
	// In the provider storage mode the guest trees are on the machine disk:
	// no host directories are shared (the docker driver uses volumes).
	var home, ws, rootUpper string
	if !cfg.ProviderStorage() {
		var err error
		if home, err = d.Storage.HomeDir(tid); err != nil {
			return vmm.Spec{}, err
		}
		if ws, err = d.Storage.WorkspaceDir(tid); err != nil {
			return vmm.Spec{}, err
		}
		// home is <mount>/home/agent; the upper image is <mount>/system/root-upper.img.
		rootUpper = filepath.Join(filepath.Dir(filepath.Dir(home)), "system", "root-upper.img")
	}
	bin, err := guestBinary(cfg)
	if err != nil {
		return vmm.Spec{}, err
	}
	images := cfg.ImagesDir()
	spec := vmm.Spec{
		CPUs: cfg.VM.CPUs, MemMB: cfg.VM.MemMB, GPUPCIAddr: cfg.VM.GPUPCIAddr,
		Kernel: filepath.Join(images, "vmlinux"), Initramfs: filepath.Join(images, "initramfs.cpio.gz"),
		Rootfs: filepath.Join(images, "rootfs.squashfs"), Image: os.Getenv(envDockerImage),
		GuestBinary: bin, HomeDir: home, WorkspaceDir: ws, WebRTCPort: webrtcPort, RootUpper: rootUpper,
	}
	if d.Driver == config.DriverCHV && !bootstrap.ImagesPresent(images) {
		if cfg.RootfsURL == "" {
			return vmm.Spec{}, fmt.Errorf("guest images are missing in %s: run 'lydiabox runtime build-rootfs' or set LYDIABOX_ROOTFS_URL", images)
		}
		if err := d.Bootstrap.FetchImages(ctx, cfg.RootfsURL, images); err != nil {
			return vmm.Spec{}, err
		}
	}
	return spec, nil
}

// guestBinary finds the linux lydiabox-guest binary injected into the VM:
// LYDIABOX_GUEST_BINARY, next to the lydiabox executable (make build puts
// both in dist/), the runtime bin directory, or PATH.
func guestBinary(cfg *config.Config) (string, error) {
	var candidates []string
	if v := os.Getenv(envGuestBinary); v != "" {
		candidates = append(candidates, v)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "lydiabox-guest"))
	}
	candidates = append(candidates, filepath.Join(cfg.BinDir(), "lydiabox-guest"))
	if p, err := exec.LookPath("lydiabox-guest"); err == nil {
		candidates = append(candidates, p)
	}
	for _, c := range candidates {
		if isELF(c) {
			return c, nil
		}
	}
	return "", errors.New("the linux lydiabox-guest binary was not found: build it with 'make build' (dist/lydiabox-guest) or set " + envGuestBinary)
}

func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return false
	}
	return bytes.Equal(magic, []byte("\x7fELF"))
}

// filesWaker is the controller as the waker of remotefs: a VM that the
// user stopped gives agentfs.ErrStopped.
type filesWaker struct{ ctl *vmm.Controller }

func (w filesWaker) EnsureRunning(ctx context.Context, tid string) error {
	err := w.ctl.EnsureRunning(ctx, tid)
	if errors.Is(err, vmm.ErrStopped) {
		return agentfs.ErrStopped
	}
	return err
}
