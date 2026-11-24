package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/harness"
	"github.com/portwatcher/lydiabox/internal/harness/adapters"
	"github.com/portwatcher/lydiabox/internal/magpie"
	"github.com/portwatcher/lydiabox/internal/magpie/wire"
	"github.com/portwatcher/lydiabox/internal/vmm"
	"github.com/portwatcher/lydiabox/internal/vmm/guestclient"
)

// wireHarness wires Phase 3 into `lydiabox up`: the host magpie gateway
// (supervisor, VM gateway key, admin client), the harness config wirers, the
// five harness adapters, the session manager, and the sessions and models
// routes.
//
// Guest glue: when wireVM (Phase 2) built the guest client and the VM
// controller, harness terminals run through them and the guest LLM tunnel
// injects the magpie gateway key. Without them (a build or test without a
// VM subsystem), the manager uses harness.UnavailableRunner, so terminal
// routes answer 503 while discovery and the models routes keep working.
func wireHarness(ctx context.Context, d *deps) (*component, error) {
	cfg := d.Config
	log := d.Logger.With("component", "harness")
	tid := cfg.TenantID

	native := cfg.GuestNative()
	if native {
		// Remove the sign-ins and keys that a host gateway of an earlier
		// mode left, before the API serves.
		var secrets MagpieSecretStore
		if d.Store != nil {
			secrets = d.Store
		}
		if err := purgeGatewayState(ctx, cfg, secrets, log); err != nil {
			log.Warn("host model gateway data not removed", "err", err)
		}
	}
	var sup *magpie.Supervisor
	if !native {
		// Guest-native mode runs no magpie: the host keeps no subscription
		// or API key.
		opts := magpie.Options{TenantID: tid, DataDir: cfg.MagpieDir(), Addr: cfg.Magpie.Addr, WebAddr: cfg.Magpie.WebAddr,
			Secrets: d.Store, Logger: d.Logger}
		if bin, err := d.Bootstrap.Resolve("magpie"); err != nil {
			opts.BinaryError = err.Error()
		} else {
			opts.Binary = bin
		}
		s, err := magpie.NewSupervisor(opts)
		if err != nil {
			return nil, err
		}
		sup = s
	}
	if d.Files == nil {
		return nil, errors.New("harness: the guest filesystem is not available")
	}
	bindings := &magpie.Bindings{Store: d.Store, Files: d.Files, Native: native}
	service := &magpie.Service{Bindings: bindings, Native: native}
	if sup != nil {
		service.Gateway = sup
	}
	bindings.DefaultModel = func(ctx context.Context) string { return gatewayDefaultModel(ctx, service) }

	runner, waker := guestRuntime(d)
	mopts := harness.ManagerOptions{
		Adapters: adapters.All(), Files: d.Files, Discovery: sessionDiscovery(d, log), Store: d.Store,
		Runner: runner, Waker: waker, Wiring: bindings, Logger: d.Logger, // the manager adds component=harness
		MCPServers: acpMCPServers(), GuestNative: native,
	}
	if cfg.ProviderStorage() && d.Store != nil {
		// The session files are on the machine disk: the host keeps a copy
		// of the events, so a session opens at once while the machine is
		// paused or stopped.
		mopts.EventCache = d.Store
	}
	mgr, err := harness.NewManager(mopts)
	if err != nil {
		return nil, err
	}
	if native {
		service.NativeUsed = nativeUsed(mgr, tid)
	}
	if mopts.EventCache != nil && d.VM != nil && d.VM.hooks != nil {
		// Before a pause or stop of the host, the watched sessions write
		// their changed copies from memory (no guest call).
		d.VM.BeforeFlush(mgr.SaveEventCopiesBeforePause)
	}

	// A pause ends the live terminals at once with "paused", also where the
	// driver keeps the connection open to a frozen guest (docker, chv).
	if d.VM != nil && d.VM.hooks != nil {
		d.VM.OnState(func(tid string, st vmm.State) {
			switch st {
			case vmm.StatePaused:
				mgr.Paused(tid)
			case vmm.StateStopped:
				// A stopped VM has no processes: detached sessions ended.
				go mgr.Stopped(context.Background(), tid)
			case vmm.StateRunning:
				// Terminals that a host restart, a lost link, or a pause
				// detached become live again; ended ones become exited.
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
					defer cancel()
					if err := mgr.Reconcile(ctx, tid); err != nil {
						log.Info("reconcile the guest terminals", "err", err)
					}
				}()
			}
		})
	}

	// The guest LLM tunnel replaces guest credentials with the gateway key.
	// In guest-native mode it refuses every request: the host routes no
	// model traffic.
	if d.VM != nil && d.VM.Gateway != nil {
		if native {
			d.VM.Gateway.Disable(gatewayOffReason)
		} else {
			d.VM.Gateway.SetInjectHeaders(sup.InjectHeaders)
		}
	}

	return &component{
		Name: "harness",
		Routes: []api.RouteRegistrar{
			api.SessionsRoutes{Manager: mgr},
			api.ModelsRoutes{Service: service},
		},
		Start: func(ctx context.Context) error {
			if sup != nil {
				if err := sup.Start(ctx); err != nil {
					return err
				}
			}
			// A VM that is stopped at host start has no processes; the
			// state hook does not run because the state did not change.
			if d.VM != nil && d.VM.Controller != nil {
				switch d.VM.Controller.State(tid) {
				case vmm.StateStopped:
					mgr.Stopped(ctx, tid)
				case vmm.StateRunning:
					// The VM ran before this host process: compare the
					// sessions with the guest now (the RUNNING hook may have
					// run before the guest link was up).
					go func() {
						rctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
						defer cancel()
						if err := mgr.Reconcile(rctx, tid); err != nil {
							log.Info("reconcile the guest terminals", "err", err)
						}
					}()
				}
			}
			// Point every harness at the gateway once the home tree is
			// mounted; a config that cannot be parsed is left untouched.
			// In the provider storage mode this would wake the machine, so
			// EnsureWired before each harness start does it instead.
			if cfg.ProviderStorage() {
				return nil
			}
			if err := bindings.WireAll(ctx, tid); err != nil {
				log.Warn("harness config wiring incomplete", "err", err)
			}
			return nil
		},
		Stop: func(ctx context.Context) error {
			// The VM may stop after this (docker, chv): the next host start
			// handles the sessions, not a hook racing the state database.
			mgr.Close()
			if sup == nil {
				return nil
			}
			return sup.Stop(ctx)
		},
	}, nil
}

// sessionDiscovery returns the session discovery of the storage mode: the
// adapters on the host JuiceFS mount, or the guest daemon with a cache of
// the last result for a paused or stopped machine.
func sessionDiscovery(d *deps, log *slog.Logger) harness.Discovery {
	if !d.Config.ProviderStorage() {
		return harness.LocalDiscovery{Dirs: d.Storage, Adapters: adapters.All(), EventReader: adapters.NewEventReader()}
	}
	g := &harness.GuestDiscovery{Cache: d.Store, Logger: log}
	if d.Guest != nil {
		g.Guest = d.Guest
	}
	if d.VM != nil && d.VM.Controller != nil {
		ctl := d.VM.Controller
		g.Running = ctl.Serving
	}
	if rfs := remoteFS(d.Files); rfs != nil {
		g.Begin = rfs.Begin
	}
	return g
}

// guestRuntime returns the PTY runner and VM waker of the harness manager.
func guestRuntime(d *deps) (harness.PTYRunner, harness.VMWaker) {
	if d.Guest == nil || d.VM == nil || d.VM.Controller == nil {
		return harness.UnavailableRunner{}, harness.NoopWaker{}
	}
	// The guest client already holds a "pty" lease per running PTY, so the
	// waker passed on exposes only EnsureRunning (no second lease).
	return guestRunner{c: d.Guest}, wakeOnly{w: d.VM.Controller}
}

// guestRunner adapts the guest daemon client to harness.PTYRunner.
type guestRunner struct{ c *guestclient.Client }

func (g guestRunner) StartPTY(ctx context.Context, tenantID string, spec harness.PTYSpec) (harness.PTY, error) {
	p, err := g.c.StartPTY(ctx, tenantID, guestclient.PTYSpec(spec))
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (g guestRunner) AttachPTY(ctx context.Context, tenantID, id string) (harness.PTY, error) {
	return attachResult(g.c.AttachPTY(ctx, tenantID, id))
}

// AttachRunningPTY implements harness.RunningAttacher.
func (g guestRunner) AttachRunningPTY(ctx context.Context, tenantID, id string) (harness.PTY, error) {
	return attachResult(g.c.AttachRunningPTY(ctx, tenantID, id))
}

// KillPTY implements harness.PTYKiller.
func (g guestRunner) KillPTY(ctx context.Context, tenantID, id string) error {
	err := g.c.KillPTY(ctx, tenantID, id)
	if rpc.IsCode(err, rpc.CodeNotFound) {
		return fmt.Errorf("%w: %v", harness.ErrNotFound, err)
	}
	return err
}

func attachResult(p guestclient.PTY, err error) (harness.PTY, error) {
	if rpc.IsCode(err, rpc.CodeNotFound) {
		// The guest has no such terminal (it ended, or the machine booted
		// fresh after a pause).
		return nil, fmt.Errorf("%w: %v", harness.ErrNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (g guestRunner) ListPTYs(ctx context.Context, tenantID string) ([]harness.PTYInfo, error) {
	infos, err := g.c.ListPTYs(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]harness.PTYInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, harness.PTYInfo{ID: i.ID, Command: i.Command, Dir: i.Dir, Running: i.Running, ExitCode: i.ExitCode, ExitedAt: i.ExitedAt})
	}
	return out, nil
}

type wakeOnly struct{ w *vmm.Controller }

func (w wakeOnly) EnsureRunning(ctx context.Context, tenantID string) error {
	return w.w.EnsureRunning(ctx, tenantID)
}

// Held implements harness.VMHolder.
func (w wakeOnly) Held(tenantID string) bool { return w.w.Held(tenantID) }

// Serving reports whether the VM runs and no pause or stop of it is in
// progress: the harness manager attaches a watched terminal again only then.
func (w wakeOnly) Serving(tenantID string) bool { return w.w.Serving(tenantID) }

// Paused implements harness.VMPauseReporter.
func (w wakeOnly) Paused(tenantID string) bool { return w.w.Paused(tenantID) }

// Start implements harness.VMStarter.
func (w wakeOnly) Start(ctx context.Context, tenantID string) error {
	return w.w.Start(ctx, tenantID)
}

var (
	_ harness.VMHolder        = wakeOnly{}
	_ harness.VMPauseReporter = wakeOnly{}
	_ harness.RunningAttacher = guestRunner{}
	_ harness.PTYKiller       = guestRunner{}
)

// reconcileTimeout bounds one reconcile of the guest terminals.
const reconcileTimeout = time.Minute

// acpMCPServers are the MCP servers that the ACP bridge passes to every
// agent: the same servers that the wirers register in the harness configs.
func acpMCPServers() []harness.MCPServer {
	out := make([]harness.MCPServer, 0, len(wire.DefaultMCPServers))
	for _, s := range wire.DefaultMCPServers {
		out = append(out, harness.MCPServer{Name: s.Name, URL: s.URL})
	}
	return out
}

// gatewayDefaultModel returns the first model of the gateway catalog, or ""
// when the gateway has none or does not answer.
func gatewayDefaultModel(ctx context.Context, s *magpie.Service) string {
	ms, err := s.ListModels(ctx)
	if err != nil || len(ms) == 0 {
		return ""
	}
	return ms[0].ID
}
