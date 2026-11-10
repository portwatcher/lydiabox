package cli

import (
	"context"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/magpie"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
)

// wireHarness wires Phase 3 into `lydiabox up`: the host magpie gateway
// (supervisor, VM gateway key, admin client), the harness config wirers, the
// five harness adapters, the session manager, the hot-switch bridge, and the
// sessions and models routes.
//
// Guest glue: when wireVM (Phase 2) built the guest client and the VM
// controller, harness terminals run through them and the guest LLM tunnel
// injects the magpie gateway key. Without them (a build or test without a
// VM subsystem), the manager uses harness.UnavailableRunner, so terminal
// routes answer 503 while discovery, the handoff preview, and the models
// routes keep working.
func wireHarness(ctx context.Context, d *deps) (*component, error) {
	cfg := d.Config
	log := d.Logger.With("component", "harness")
	tid := cfg.TenantID

	opts := magpie.Options{TenantID: tid, DataDir: cfg.MagpieDir(), Secrets: d.Store, Logger: d.Logger}
	if bin, err := d.Bootstrap.Resolve("magpie"); err != nil {
		opts.BinaryError = err.Error()
	} else {
		opts.Binary = bin
	}
	sup, err := magpie.NewSupervisor(opts)
	if err != nil {
		return nil, err
	}
	bindings := &magpie.Bindings{Store: d.Store, Homes: d.Storage}
	service := &magpie.Service{Gateway: sup, Bindings: bindings}

	runner, waker := guestRuntime(d)
	mgr, err := harness.NewManager(harness.ManagerOptions{
		Adapters: adapters.All(), Dirs: d.Storage, Store: d.Store,
		Runner: runner, Waker: waker, Wiring: bindings, Logger: log,
	})
	if err != nil {
		return nil, err
	}
	bridge := &harness.Bridge{Manager: mgr, Git: harness.ExecGit{}, Binder: bindings, Logger: log}

	// The guest LLM tunnel replaces guest credentials with the gateway key.
	if d.VM != nil && d.VM.Gateway != nil {
		d.VM.Gateway.SetInjectHeaders(sup.InjectHeaders)
	}

	return &component{
		Name: "harness",
		Routes: []api.RouteRegistrar{
			api.SessionsRoutes{Manager: mgr, Bridge: bridge},
			api.ModelsRoutes{Service: service},
		},
		Start: func(ctx context.Context) error {
			if err := sup.Start(ctx); err != nil {
				return err
			}
			// Point every harness at the gateway once the home tree is
			// mounted; a config that cannot be parsed is left untouched.
			if err := bindings.WireAll(ctx, tid); err != nil {
				log.Warn("harness config wiring incomplete", "err", err)
			}
			return nil
		},
		Stop: sup.Stop,
	}, nil
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
	p, err := g.c.AttachPTY(ctx, tenantID, id)
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
		out = append(out, harness.PTYInfo{ID: i.ID, Command: i.Command, Dir: i.Dir, Running: i.Running, ExitCode: i.ExitCode})
	}
	return out, nil
}

type wakeOnly struct{ w harness.VMWaker }

func (w wakeOnly) EnsureRunning(ctx context.Context, tenantID string) error {
	return w.w.EnsureRunning(ctx, tenantID)
}
