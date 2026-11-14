package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/bootstrap"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/storage/files"
	"github.com/lydiabox/lydiabox/internal/store"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
	"github.com/lydiabox/lydiabox/internal/webui"
)

// deps are the shared services built by `lydiabox up` and handed to every
// subsystem wiring function (wire_<subsystem>.go).
type deps struct {
	Config    *config.Config
	Driver    string
	Logger    *slog.Logger
	Store     *store.Store
	Bootstrap *bootstrap.Bootstrapper
	Storage   storage.Provider
	Auth      *api.Authenticator
	// VMStatus feeds GET /api/v1/status. Phase 2 replaces it in wireVM.
	VMStatus api.VMStatusSource
	// VM and Guest are set by wireVM (Phase 2): the VM controller, driver,
	// and gateway tunnel, and the guest daemon client (PTYs, MCP tools).
	VM    *vmRuntime
	Guest *guestclient.Client
	// SkipBootstrap is `lydiabox up -skip-bootstrap`: install nothing.
	SkipBootstrap bool
}

// component is one subsystem wired into `lydiabox up`: its routes and an
// optional lifecycle. Start runs after storage is mounted; Stop runs before
// storage is unmounted, in reverse wiring order.
type component struct {
	Name   string
	Routes []api.RouteRegistrar
	Start  func(ctx context.Context) error
	Stop   func(ctx context.Context) error
}

// wirers lists the subsystem wiring functions in start order. Each later
// phase implements its function in its own wire_<subsystem>.go file.
var wirers = []func(ctx context.Context, d *deps) (*component, error){
	wireRemote,    // remote access: internal/cli/wire_remote.go (first: it can move the listen address)
	wireVM,        // Phase 2: internal/cli/wire_vm.go
	wireHarness,   // Phase 3: internal/cli/wire_harness.go
	wireConnector, // Phase 4: internal/cli/wire_connector.go
	wireDesktop,   // Phase 2 and 5: internal/cli/wire_desktop.go
}

func runUp(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet("up", env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "do not download missing runtime binaries")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	cfg, err := config.Load(config.LoadOptions{ConfigFile: *configFile})
	if err != nil {
		return err
	}
	if err := cfg.ValidateServe(); err != nil {
		return err
	}
	if err := cfg.EnsureSecrets(); err != nil {
		return err
	}
	log := newLogger(stderrOrDiscard(env), cfg.LogLevel)
	if cfg.AuthTokenGenerated {
		fmt.Fprintf(env.Stdout, "Generated admin token (saved to %s):\n\n    %s\n\n", cfg.AuthTokenPath(), cfg.AuthToken)
	}
	if cfg.MasterKeyGenerated {
		log.Warn("generated a new master key; back it up, it decrypts stored connector tokens", "path", cfg.MasterKeyPath())
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serve(ctx, env, cfg, log, *skipBootstrap)
}

func serve(ctx context.Context, env Env, cfg *config.Config, log *slog.Logger, skipBootstrap bool) error {
	driver := cfg.EffectiveVMDriver()
	log.Info("starting LydiaBox", "dataDir", cfg.DataDir, "vmDriver", driver, "tenant", cfg.TenantID)

	key, err := cfg.MasterKeyBytes()
	if err != nil {
		return err
	}
	objects, err := storage.NewS3Store(cfg.S3, nil)
	if err != nil {
		return err
	}
	restored, err := storage.RestoreState(ctx, objects, cfg.TenantID, cfg.DBPath(), log)
	if err != nil {
		return err
	}
	if restored && cfg.MasterKeyGenerated {
		log.Warn("the state database was restored but the master key is new; stored connector tokens cannot be decrypted. "+
			"Stop LydiaBox, copy the old master key to this path, and start again, or reconnect the connectors",
			"path", cfg.MasterKeyPath())
	}
	st, err := store.Open(ctx, cfg.DBPath(), key)
	if err != nil {
		return err
	}
	defer st.Close()

	boot, err := bootstrap.New(bootstrap.Options{BinDir: cfg.BinDir(), Driver: driver, Mirrors: cfg.Mirrors, Logger: log})
	if err != nil {
		return err
	}
	if !skipBootstrap {
		if _, err := boot.Ensure(ctx); err != nil {
			return err
		}
	}
	juicefs, err := boot.Resolve("juicefs")
	if err != nil {
		return err
	}
	runner := storage.OSRunner{}
	sup, err := storage.NewSupervisor(storage.Options{
		Config: cfg, JuiceFSBinary: juicefs, Runner: runner, Objects: objects, State: st,
		Logger: log, RequireRootUpper: driver == config.DriverCHV,
	})
	if err != nil {
		return err
	}
	dav, err := storage.NewWebDAV(storage.WebDAVOptions{Config: cfg, JuiceFSBinary: juicefs, Runner: runner, Logger: log})
	if err != nil {
		return err
	}
	s3gw, err := storage.NewS3Gateway(storage.S3GatewayOptions{Config: cfg, JuiceFSBinary: juicefs, Runner: runner, Logger: log})
	if err != nil {
		return err
	}

	d := &deps{
		Config: cfg, Driver: driver, Logger: log, Store: st, Bootstrap: boot, Storage: sup,
		Auth:          api.NewAuthenticator(cfg.AuthToken),
		VMStatus:      api.StaticVMStatus{State: api.VMStopped, Driver: driver},
		SkipBootstrap: skipBootstrap,
	}
	var comps []*component
	for _, wire := range wirers {
		c, err := wire(ctx, d)
		if err != nil {
			return err
		}
		if c != nil {
			comps = append(comps, c)
		}
	}

	regs := []api.RouteRegistrar{
		api.CoreRoutes{Auth: d.Auth, VM: d.VMStatus, Storage: sup, S3Gateway: s3gw},
		api.FilesRoutes{Handler: files.NewHandler(files.NewService(sup, files.Options{}))},
		api.WebDAVRoutes{Handler: dav.Handler()},
		api.S3Routes{Handler: s3gw.Handler()},
	}
	for _, c := range comps {
		regs = append(regs, c.Routes...)
	}
	regs = append(regs, api.WebUIRoutes{Handler: webui.Handler(webui.FS())})
	srv, err := api.NewServer(api.Options{Addr: cfg.ListenAddr, Auth: d.Auth, TenantID: cfg.TenantID, Logger: log}, regs...)
	if err != nil {
		return err
	}
	addr, err := srv.Listen()
	if err != nil {
		return err
	}

	shutdown := newShutdown(log)
	defer shutdown.run()
	if err := sup.Start(ctx, cfg.TenantID); err != nil {
		return err
	}
	shutdown.push("storage", func(ctx context.Context) error { return sup.Stop(ctx, cfg.TenantID) })
	if err := dav.Start(ctx, cfg.TenantID); err != nil {
		return err
	}
	shutdown.push("webdav", func(ctx context.Context) error { return dav.Stop(ctx, cfg.TenantID) })
	if err := s3gw.Start(ctx, cfg.TenantID); err != nil {
		return err
	}
	shutdown.push("s3 gateway", func(ctx context.Context) error { return s3gw.Stop(ctx, cfg.TenantID) })
	for _, c := range comps {
		if c.Start != nil {
			if err := c.Start(ctx); err != nil {
				return fmt.Errorf("%s: %w", c.Name, err)
			}
		}
		if c.Stop != nil {
			shutdown.push(c.Name, c.Stop)
		}
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	shutdown.push("http", srv.Shutdown)
	fmt.Fprintf(env.Stdout, "LydiaBox is listening on http://%s (web UI built: %t)\n", addr, webui.Built())
	if s3gw.Enabled() {
		fmt.Fprintf(env.Stdout, "S3 gateway: endpoint http://%s%s, bucket %q (path style), access key %q, secret key = admin token\n",
			addr, storage.S3GatewayPrefix, storage.S3GatewayBucket, storage.S3GatewayAccessKey)
	}

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		return nil
	case err := <-serveErr:
		return err
	}
}

// shutdownStack stops services in reverse start order with one overall
// deadline long enough for a JuiceFS writeback flush.
type shutdownStack struct {
	log   *slog.Logger
	steps []shutdownStep
}

type shutdownStep struct {
	name string
	fn   func(context.Context) error
}

func newShutdown(log *slog.Logger) *shutdownStack { return &shutdownStack{log: log} }

func (s *shutdownStack) push(name string, fn func(context.Context) error) {
	s.steps = append(s.steps, shutdownStep{name, fn})
}

func (s *shutdownStack) run() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var errs []error
	for i := len(s.steps) - 1; i >= 0; i-- {
		step := s.steps[i]
		if err := step.fn(ctx); err != nil {
			s.log.Error("shutdown step failed", "step", step.name, "err", err)
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 && len(s.steps) > 0 {
		s.log.Info("shutdown complete")
	}
	_ = errors.Join(errs...)
}
