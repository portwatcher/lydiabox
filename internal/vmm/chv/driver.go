package chv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// Options configures the driver.
type Options struct {
	// RunDir holds per-tenant sockets and logs ($DATA_DIR/run).
	RunDir string
	// CloudHypervisor and Virtiofsd are the binaries (bootstrap.Resolve).
	CloudHypervisor string
	Virtiofsd       string
	// MkfsExt4 builds the guest binary image (default "mkfs.ext4").
	MkfsExt4 string
	Runner   storage.Runner
	LookPath func(string) (string, error)
	// Network returns the guest network of a tenant (default
	// DefaultNetwork). Return Network{Disabled: true} to run without a NIC.
	Network func(tenantID string) Network
	// StartTimeout bounds waiting for the daemons' sockets (default 15 s).
	StartTimeout time.Duration
	Logger       *slog.Logger
}

// Driver implements vmm.Driver with Cloud Hypervisor.
type Driver struct {
	opts Options
	fw   Firewall

	mu  sync.Mutex
	vms map[string]*instance
}

type instance struct {
	paths   Paths
	api     *Client
	network Network
	webrtc  int
	chv     *proc
	vfs     []*proc
	logFile *os.File
}

type proc struct {
	p    storage.Process
	done chan struct{}
}

func watch(p storage.Process) *proc {
	w := &proc{p: p, done: make(chan struct{})}
	go func() {
		_ = p.Wait()
		close(w.done)
	}()
	return w
}

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stop sends SIGTERM and kills after grace.
func (p *proc) stop(grace time.Duration) {
	if p == nil || p.exited() {
		return
	}
	_ = p.p.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(grace):
		_ = p.p.Kill()
		<-p.done
	}
}

// New returns a driver.
func New(opts Options) (*Driver, error) {
	if opts.RunDir == "" || opts.CloudHypervisor == "" || opts.Virtiofsd == "" || opts.Runner == nil {
		return nil, errors.New("chv: RunDir, CloudHypervisor, Virtiofsd, and Runner are required")
	}
	if opts.MkfsExt4 == "" {
		opts.MkfsExt4 = "mkfs.ext4"
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Network == nil {
		opts.Network = DefaultNetwork
	}
	if opts.StartTimeout <= 0 {
		opts.StartTimeout = 15 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Driver{opts: opts, fw: Firewall{Runner: opts.Runner, LookPath: opts.LookPath}, vms: map[string]*instance{}}, nil
}

// Name implements vmm.Driver.
func (d *Driver) Name() string { return "chv" }

func (d *Driver) paths(tid string) (Paths, error) {
	if err := vmm.ValidTenantID(tid); err != nil {
		return Paths{}, err
	}
	return PathsFor(d.opts.RunDir, tid), nil
}

// instance returns the tracked VM, adopting a cloud-hypervisor process
// that answers on the API socket (for example after a host restart).
func (d *Driver) instance(ctx context.Context, tid string) (*instance, error) {
	p, err := d.paths(tid)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if in := d.vms[tid]; in != nil {
		return in, nil
	}
	api := NewClient(p.APISocket)
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := os.Stat(p.APISocket); err != nil || api.Ping(pctx) != nil {
		return nil, nil
	}
	in := &instance{paths: p, api: api, network: d.opts.Network(tid)}
	d.vms[tid] = in
	return in, nil
}

// Create implements vmm.Driver.
func (d *Driver) Create(ctx context.Context, tid string, spec vmm.Spec) (err error) {
	p, err := d.paths(tid)
	if err != nil {
		return err
	}
	if in, _ := d.instance(ctx, tid); in != nil {
		if err := d.Shutdown(ctx, tid, true); err != nil {
			return fmt.Errorf("chv: clean up previous VM: %w", err)
		}
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	if spec.GuestBinary == "" {
		return errors.New("chv: the lydiabox-guest binary path is required")
	}
	if err := EnsureGuestImage(ctx, d.opts.Runner, d.opts.MkfsExt4, spec.GuestBinary, p.GuestImage); err != nil {
		return err
	}
	nw := d.opts.Network(tid)
	cfg, err := BuildConfig(spec, p, nw, tid)
	if err != nil {
		return err
	}
	for _, s := range []string{p.APISocket, p.VFSHome, p.VFSWorkspace, p.Vsock} {
		_ = os.Remove(s)
	}
	in := &instance{paths: p, api: NewClient(p.APISocket), network: nw, webrtc: spec.WebRTCPort}
	defer func() {
		if err != nil {
			d.teardown(context.Background(), in)
		}
	}()
	for _, share := range []struct{ sock, dir string }{{p.VFSHome, spec.HomeDir}, {p.VFSWorkspace, spec.WorkspaceDir}} {
		pr, err := d.opts.Runner.Start(storage.Command{Path: d.opts.Virtiofsd, Args: []string{
			"--socket-path=" + share.sock, "--shared-dir=" + share.dir, "--cache=auto",
		}, Stderr: os.Stderr})
		if err != nil {
			return fmt.Errorf("chv: start virtiofsd for %s: %w", share.dir, err)
		}
		w := watch(pr)
		in.vfs = append(in.vfs, w)
		if err := d.waitSocket(ctx, share.sock, w); err != nil {
			return fmt.Errorf("chv: virtiofsd %s: %w", share.dir, err)
		}
	}
	if !nw.Disabled {
		if err := d.fw.Setup(ctx, nw, spec.WebRTCPort); err != nil {
			return err
		}
	}
	logf, err := os.OpenFile(p.VMMLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	in.logFile = logf
	pr, err := d.opts.Runner.Start(storage.Command{Path: d.opts.CloudHypervisor,
		Args: []string{"--api-socket", "path=" + p.APISocket}, Stdout: logf, Stderr: logf})
	if err != nil {
		return fmt.Errorf("chv: start cloud-hypervisor: %w", err)
	}
	in.chv = watch(pr)
	if err := d.waitSocket(ctx, p.APISocket, in.chv); err != nil {
		return fmt.Errorf("chv: cloud-hypervisor API: %w", err)
	}
	if err := d.waitAPI(ctx, in); err != nil {
		return err
	}
	if err := in.api.CreateVM(ctx, cfg); err != nil {
		return err
	}
	d.mu.Lock()
	d.vms[tid] = in
	d.mu.Unlock()
	return nil
}

func (d *Driver) waitSocket(ctx context.Context, path string, w *proc) error {
	deadline := time.Now().Add(d.opts.StartTimeout)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if w != nil && w.exited() {
			return fmt.Errorf("process exited before creating %s", path)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (d *Driver) waitAPI(ctx context.Context, in *instance) error {
	deadline := time.Now().Add(d.opts.StartTimeout)
	for {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		err := in.api.Ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("chv: cloud-hypervisor API not ready: %w", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (d *Driver) running(ctx context.Context, tid string) (*instance, error) {
	in, err := d.instance(ctx, tid)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, fmt.Errorf("%w: no VM for tenant %q", vmm.ErrUnavailable, tid)
	}
	return in, nil
}

// Boot implements vmm.Driver.
func (d *Driver) Boot(ctx context.Context, tid string) error {
	in, err := d.running(ctx, tid)
	if err != nil {
		return err
	}
	return in.api.Boot(ctx)
}

// Pause implements vmm.Driver.
func (d *Driver) Pause(ctx context.Context, tid string) error {
	in, err := d.running(ctx, tid)
	if err != nil {
		return err
	}
	return in.api.Pause(ctx)
}

// Resume implements vmm.Driver.
func (d *Driver) Resume(ctx context.Context, tid string) error {
	in, err := d.running(ctx, tid)
	if err != nil {
		return err
	}
	return in.api.Resume(ctx)
}

// Shutdown implements vmm.Driver: vm.shutdown, vmm.shutdown, then the
// virtiofsd daemons and the network rules. The controller syncs guest
// filesystems first; force only shortens the grace periods.
func (d *Driver) Shutdown(ctx context.Context, tid string, force bool) error {
	in, err := d.instance(ctx, tid)
	if err != nil {
		return err
	}
	if in == nil {
		return nil
	}
	if !force {
		if err := in.api.ShutdownVM(ctx); err != nil {
			var ae *APIError
			if !errors.As(err, &ae) {
				d.opts.Logger.Warn("vm.shutdown failed", "tenant", tid, "err", err)
			}
		}
	} else {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = in.api.ShutdownVM(sctx)
		cancel()
	}
	d.teardown(ctx, in)
	d.mu.Lock()
	if d.vms[tid] == in {
		delete(d.vms, tid)
	}
	d.mu.Unlock()
	return nil
}

func (d *Driver) teardown(ctx context.Context, in *instance) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_ = in.api.ShutdownVMM(sctx)
	cancel()
	if in.chv != nil {
		select {
		case <-in.chv.done:
		case <-time.After(10 * time.Second):
			in.chv.stop(2 * time.Second)
		}
	}
	for _, v := range in.vfs {
		v.stop(5 * time.Second)
	}
	if !in.network.Disabled {
		if err := d.fw.Teardown(ctx, in.network, in.webrtc); err != nil {
			d.opts.Logger.Warn("remove guest network rules", "err", err)
		}
	}
	if in.logFile != nil {
		in.logFile.Close()
	}
	for _, s := range []string{in.paths.APISocket, in.paths.VFSHome, in.paths.VFSWorkspace, in.paths.Vsock} {
		_ = os.Remove(s)
	}
}

// State implements vmm.Driver.
func (d *Driver) State(ctx context.Context, tid string) (vmm.State, error) {
	in, err := d.instance(ctx, tid)
	if err != nil || in == nil {
		return vmm.StateStopped, err
	}
	if in.chv != nil && in.chv.exited() {
		return vmm.StateStopped, nil
	}
	info, err := in.api.Info(ctx)
	if err != nil {
		// No VM created in this VMM yet, or the VMM is going away.
		return vmm.StateStopped, nil
	}
	switch info.State {
	case "Running":
		return vmm.StateRunning, nil
	case "Paused":
		return vmm.StatePaused, nil
	}
	return vmm.StateStopped, nil
}

// DialGuest implements vmm.Driver with the CONNECT handshake.
func (d *Driver) DialGuest(ctx context.Context, tid string, port uint32) (net.Conn, error) {
	p, err := d.paths(tid)
	if err != nil {
		return nil, err
	}
	return DialVsock(ctx, p.Vsock, port)
}

// ListenGuestPort implements vmm.Driver: Cloud Hypervisor forwards guest
// connections to host port P to the UNIX socket <vsock.sock>_P.
func (d *Driver) ListenGuestPort(_ context.Context, tid string, port uint32) (net.Listener, error) {
	p, err := d.paths(tid)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return nil, err
	}
	return rpc.ListenUnix(ListenerPath(p.Vsock, port), 0o600)
}
