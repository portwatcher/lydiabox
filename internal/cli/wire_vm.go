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
	"strconv"

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
	webrtcPort, err := webRTCPort(defaultWebRTCPort(d))
	if err != nil {
		return nil, err
	}
	drv, err := newDriver(d, log)
	if err != nil {
		return nil, err
	}
	guest := guestclient.New(guestclient.Options{Dialer: drv, Logger: log})
	ctl, err := vmm.NewController(vmm.Options{
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
	gw, err := credproxy.NewHostGateway(os.Getenv(envGatewayUpstream))
	if err != nil {
		return nil, err
	}
	d.VM = &vmRuntime{Controller: ctl, Driver: drv, Gateway: gw}
	d.Guest = guest
	d.VMStatus = ctl

	gwCtx, gwCancel := context.WithCancel(context.Background())
	tid := cfg.TenantID
	return &component{
		Name:   "vm",
		Routes: []api.RouteRegistrar{api.VMRoutes{Controller: ctl}},
		Start: func(ctx context.Context) error {
			if err := ctl.Refresh(ctx, tid); err != nil {
				log.Warn("read VM state", "err", err)
			}
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
			return ctl.Close(ctx)
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

func webRTCPort(def int) (int, error) {
	v := os.Getenv(rpc.EnvWebRTCPort)
	if v == "" {
		return def, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 0 || p > 65535 {
		return 0, fmt.Errorf("%s must be a UDP port (0 disables forwarding), got %q", rpc.EnvWebRTCPort, v)
	}
	return p, nil
}

func newDriver(d *deps, log interface{ Warn(string, ...any) }) (vmm.Driver, error) {
	cfg := d.Config
	runner := storage.OSRunner{}
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
			// "default" keeps 172.30.0.0/24; other tenants get a stable 172.30.<n>.0/24.
			Network: func(ctx context.Context, tid string) (chv.Network, error) {
				n, err := d.Store.TenantSubnet(ctx, tid)
				if err != nil {
					return chv.Network{}, err
				}
				return chv.TenantNetwork(tid, n), nil
			},
		})
	}
	return docker.New(docker.Options{RunDir: cfg.RunDir(), Image: os.Getenv(envDockerImage), Runner: runner,
		Logger: d.Logger.With("component", "docker")})
}

// vmSpec assembles the VM spec at boot time, fetching guest images from
// LYDIABOX_ROOTFS_URL when they are missing (chv).
func vmSpec(ctx context.Context, d *deps, tid string, webrtcPort int) (vmm.Spec, error) {
	cfg := d.Config
	home, err := d.Storage.HomeDir(tid)
	if err != nil {
		return vmm.Spec{}, err
	}
	ws, err := d.Storage.WorkspaceDir(tid)
	if err != nil {
		return vmm.Spec{}, err
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
		GuestBinary: bin, HomeDir: home, WorkspaceDir: ws, WebRTCPort: webrtcPort,
		// home is <mount>/home/agent; the upper image is <mount>/system/root-upper.img.
		RootUpper: filepath.Join(filepath.Dir(filepath.Dir(home)), "system", "root-upper.img"),
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
