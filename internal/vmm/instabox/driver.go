// Package instabox is the vmm.Driver of InstaBox machines
// (https://instabox.sh): one hosted Firecracker machine for each tenant,
// with the agent files on its persistent /data disk (the provider storage
// mode). The driver makes or adopts the machine through the InstaBox API
// (internal/instabox), writes the guest settings to the machine, and
// reaches the guest daemon over the guest link (internal/vmm/link): a
// WebSocket to a private port of the machine that carries the guest RPC
// and the reverse channels 9001 and 9002.
//
// Power: the cell controller owns the power state. While the driver is
// attached to a running machine, it holds an InstaBox activity lease, so
// that the provider's idle pause does not pause a machine that the cell
// uses; the core idle timeout (LYDIABOX_IDLE_TIMEOUT) pauses it. The
// machine's own idle pause (LYDIABOX_INSTABOX_IDLE_PAUSE) is a safety net
// for a cell that stopped. When InstaBox pauses or resumes the machine by
// itself, the state watcher reports it to the controller (WatchState).
//
// The driver never deletes a machine or its disk; only the operator
// command `lydiabox instabox destroy` does.
package instabox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	instaboximage "github.com/lydiabox/lydiabox/deploy/instabox"
	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
	"github.com/lydiabox/lydiabox/internal/vmm/link"
)

// DriverName is the vmm.Driver name and the store record key.
const DriverName = "instabox"

// Defaults.
const (
	// DefaultLinkPort is the guest port of the link listener; the driver
	// publishes it as a private InstaBox port.
	DefaultLinkPort = 9000
	// DefaultPollInterval is the period of the state watcher.
	DefaultPollInterval = 30 * time.Second
	// DefaultLeaseTTL and DefaultLeaseRenew keep the activity lease.
	DefaultLeaseTTL   = 15 * time.Minute
	DefaultLeaseRenew = 5 * time.Minute
	// DefaultReadyProbe bounds one guest probe: a request through the
	// port of a paused machine waits up to 45 s for the wake.
	DefaultReadyProbe = 60 * time.Second
	// DefaultProviderTimeout bounds one start or resume at InstaBox
	// (vmm.Capabilities.ProviderTimeout). A resume usually takes 2 to 9 s,
	// but 60 s, 115 s, and 200 s were measured.
	DefaultProviderTimeout = 10 * time.Minute
	// LeaseID is the activity lease that the cell holds.
	LeaseID = "lydiabox-cell"
	// guestEnvPath is the settings file that the image's entry point reads.
	guestEnvPath = "/data/.lydiabox/guest.env"
	// restartGuest restarts the guest daemon after guest.env changed.
	restartGuest = "sudo supervisorctl -c /etc/lydiabox/instabox-supervisord.conf restart lydiabox-guest"
)

// Options configures the driver.
type Options struct {
	// Client calls the InstaBox API. Required.
	Client *ib.Client
	// Store keeps the machine record of each tenant. Required.
	Store Store
	// Snapshot is the ID (or name) of the guest image snapshot. Required.
	Snapshot string
	// IdlePause is the idle pause of the machine (0 turns it off).
	IdlePause time.Duration
	// LinkPort is the guest link port (default 9000).
	LinkPort int
	// PollInterval is the period of the state watcher (default 30 s).
	PollInterval time.Duration
	// LeaseTTL and LeaseRenew keep the activity lease (default 15 and 5
	// minutes).
	LeaseTTL   time.Duration
	LeaseRenew time.Duration
	// ProviderTimeout bounds one start or resume (default 10 minutes).
	ProviderTimeout time.Duration
	// BlockTime is how long an account error stops new attempts (default
	// 30 s).
	BlockTime time.Duration
	// LinkHTTPClient dials the guest link (default http.DefaultClient).
	LinkHTTPClient *http.Client
	// LinkWakeTimeout, LinkMinBackoff, LinkMaxBackoff, and LinkKeepAlive
	// tune the guest link (link.Options).
	LinkWakeTimeout time.Duration
	LinkMinBackoff  time.Duration
	LinkMaxBackoff  time.Duration
	LinkKeepAlive   time.Duration
	// GuestStatusAfter is how long the port proxy answers 502 before the
	// driver logs one status line of the image's programs (default 45 s).
	GuestStatusAfter time.Duration
	// GuestBinary is the host path of the linux/amd64 lydiabox-guest
	// binary. When a machine has another guest binary, the driver
	// installs this one (upgrade.go). Empty: no guest upgrade.
	GuestBinary string
	// NoSystemUpgrade turns the system upgrade off (system.go). By
	// default the driver installs the system bundle of this build
	// (deploy/instabox) on machines that have another one.
	NoSystemUpgrade bool
	// SystemBundle replaces the bundle of this build (tests).
	SystemBundle *instaboximage.Bundle
	Logger       *slog.Logger
	Now          func() time.Time
}

// Driver implements vmm.Driver on InstaBox machines.
type Driver struct {
	opts  Options
	links *link.Links
	gate  gate
	// recMu serializes the changes of the stored records.
	recMu sync.Mutex
	// guestOnce reads the host's guest binary (guest) once.
	guestOnce sync.Once
	guest     *hostGuest
	// systemOnce makes the system bundle (system) once; systemFailed
	// holds the system plans that failed in this process (guarded by mu).
	systemOnce   sync.Once
	system       *instaboximage.Bundle
	systemFailed map[string]bool

	mu      sync.Mutex
	tenants map[string]*tenant
	watch   func(string, vmm.State)
	kick    chan struct{}
	stop    context.CancelFunc
	done    chan struct{}
	closed  bool
}

// tenant is the run-time state of one tenant.
type tenant struct {
	// op serializes the lifecycle calls and the attach of the tenant.
	op sync.Mutex

	// The fields below are guarded by Driver.mu.
	machineID string
	attached  bool
	// down: this process paused or stopped the machine, or saw it pause
	// itself. DialGuest then fails at once instead of attaching.
	down bool
	// reported is the last state the watcher saw.
	reported vmm.State
	lease    *leaseLoop
	// statusTimer and statusLogged: the status line after a 502 of the
	// port proxy (diagnose.go), once for each connection of the link.
	statusTimer  *time.Timer
	statusLogged bool
}

// New validates opts and returns a driver.
func New(opts Options) (*Driver, error) {
	if opts.Client == nil || opts.Store == nil {
		return nil, errors.New("instabox: Client and Store are required")
	}
	if opts.Snapshot == "" {
		return nil, errors.New("instabox: a snapshot is required (LYDIABOX_INSTABOX_SNAPSHOT)")
	}
	if opts.LinkPort == 0 {
		opts.LinkPort = DefaultLinkPort
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = DefaultLeaseTTL
	}
	if opts.LeaseRenew <= 0 || opts.LeaseRenew >= opts.LeaseTTL {
		opts.LeaseRenew = opts.LeaseTTL / 3
	}
	if opts.BlockTime <= 0 {
		opts.BlockTime = DefaultBlockTime
	}
	if opts.ProviderTimeout <= 0 {
		opts.ProviderTimeout = DefaultProviderTimeout
	}
	if opts.LinkMinBackoff <= 0 {
		// A reconnect through the port wakes an idle-paused machine; the
		// watcher looks first.
		opts.LinkMinBackoff = time.Second
	}
	if opts.GuestStatusAfter <= 0 {
		opts.GuestStatusAfter = DefaultGuestStatusAfter
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	d := &Driver{opts: opts, tenants: map[string]*tenant{}, kick: make(chan struct{}, 1), systemFailed: map[string]bool{}}
	d.links = link.NewLinks(d.linkOptions)
	return d, nil
}

// Name implements vmm.Driver.
func (d *Driver) Name() string { return DriverName }

// Capabilities implements vmm.CapabilityReporter.
func (d *Driver) Capabilities() vmm.Capabilities {
	return vmm.Capabilities{Remote: true, SelfPause: true, ReadyProbeTimeout: DefaultReadyProbe, ProviderTimeout: d.opts.ProviderTimeout}
}

func (d *Driver) tenant(tid string) *tenant {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := d.tenants[tid]
	if t == nil {
		t = &tenant{}
		d.tenants[tid] = t
	}
	return t
}

// linkOptions gives the link of a tenant its secret. The secret is made
// and stored on first use, before the machine exists, because the reverse
// channel listeners are made at start.
func (d *Driver) linkOptions(tid string) link.Options {
	opts := link.Options{
		WakeTimeout: d.opts.LinkWakeTimeout, MinBackoff: d.opts.LinkMinBackoff, MaxBackoff: d.opts.LinkMaxBackoff,
		KeepAlive: d.opts.LinkKeepAlive, HTTPClient: d.opts.LinkHTTPClient,
		Logger:  d.opts.Logger.With("tenant", tid),
		OnState: func(s link.Status) { d.linkState(tid, s) },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sec, err := d.ensureSecret(ctx, tid)
	if err != nil {
		d.opts.Logger.Error("instabox: read the link secret", "tenant", tid, "err", err)
		return opts
	}
	opts.Secret = []byte(sec.LinkSecret)
	return opts
}

// ensureSecret returns the stored secrets of a tenant and makes the link
// secret when there is none.
func (d *Driver) ensureSecret(ctx context.Context, tid string) (recordSecrets, error) {
	_, sec, err := d.update(ctx, tid, func(*Record, *recordSecrets) {})
	return sec, err
}

// DialGuest implements vmm.Driver. The first dial after a start of this
// process attaches to a running machine (port, settings, link).
func (d *Driver) DialGuest(ctx context.Context, tid string, port uint32) (net.Conn, error) {
	t := d.tenant(tid)
	d.mu.Lock()
	need := !t.attached && !t.down
	d.mu.Unlock()
	if need {
		if err := d.attachRunning(ctx, tid); err != nil {
			return nil, fmt.Errorf("%w: %v", vmm.ErrUnavailable, err)
		}
	}
	c, err := d.links.DialGuest(ctx, tid, port)
	return c, d.dialError(err)
}

// ListenGuestPort implements vmm.Driver.
func (d *Driver) ListenGuestPort(ctx context.Context, tid string, port uint32) (net.Listener, error) {
	return d.links.ListenGuestPort(ctx, tid, port)
}

// State implements vmm.Driver.
func (d *Driver) State(ctx context.Context, tid string) (vmm.State, error) {
	if err := d.gate.check(tid, d.opts.Now()); err != nil {
		return vmm.StateStopped, err
	}
	rec, _, _, err := loadRecord(ctx, d.opts.Store, tid)
	if err != nil {
		return vmm.StateStopped, err
	}
	if rec.MachineID == "" {
		// No machine yet: the next boot makes it.
		return vmm.StateStopped, nil
	}
	m, err := d.opts.Client.GetMachine(ctx, rec.MachineID)
	if err != nil {
		err = d.fail(tid, err)
		return vmm.StateStopped, err
	}
	d.gate.record(tid, nil)
	if m.State == ib.StateDeleted || m.State == ib.StateDeleting {
		return vmm.StateStopped, fmt.Errorf("%w: %w", vmm.ErrUnavailable, ErrMachineGone)
	}
	return mapState(m), nil
}

// fail classifies an API error and closes the gate on account errors.
func (d *Driver) fail(tid string, err error) error {
	err = classify(err, d.opts.Now(), d.opts.BlockTime)
	d.gate.record(tid, err)
	return err
}

// mapState maps an InstaBox state to a power state. A machine that runs a
// lifecycle operation reports the state it moves to.
func mapState(m *ib.Machine) vmm.State {
	switch m.State {
	case ib.StateRunning:
		return vmm.StateRunning
	case ib.StateCreating, ib.StateStarting, ib.StateResuming:
		return vmm.StateBooting
	case ib.StateSuspending, ib.StateSuspended:
		return vmm.StatePaused
	}
	// stopping, stopped, error, deleting, deleted
	return vmm.StateStopped
}

// Close stops the watcher and the activity leases, and closes the links.
// It leaves the machines as they are: the next process adopts them, and
// their idle pause applies.
func (d *Driver) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	d.stopStatusTimersLocked()
	stop, done := d.stop, d.done
	var leases []*leaseLoop
	for _, t := range d.tenants {
		if t.lease != nil {
			leases = append(leases, t.lease)
			t.lease = nil
		}
	}
	d.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	for _, l := range leases {
		l.stop(true)
	}
	return d.links.Close()
}

// machineID returns the recorded machine of a tenant.
func (d *Driver) machineID(ctx context.Context, tid string) (string, error) {
	rec, _, _, err := loadRecord(ctx, d.opts.Store, tid)
	if err != nil {
		return "", err
	}
	if rec.MachineID == "" {
		return "", fmt.Errorf("%w: the machine of tenant %s was not made yet", vmm.ErrUnavailable, tid)
	}
	t := d.tenant(tid)
	d.mu.Lock()
	t.machineID = rec.MachineID
	d.mu.Unlock()
	return rec.MachineID, nil
}
