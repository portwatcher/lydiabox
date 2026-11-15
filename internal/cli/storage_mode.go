package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/bootstrap"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/store"
)

// storageRuntime is the storage of one storage mode: the storage.Provider,
// the gateway routes, and the services that start before the subsystems.
type storageRuntime struct {
	Provider storage.Provider
	// Files is the guest filesystem in the juicefs mode; the provider mode
	// sets it in wireVM (it needs the guest client).
	Files    agentfs.FS
	Routes   []api.RouteRegistrar
	Gateway  api.S3GatewayStatusSource
	services []service
	s3gw     *storage.S3Gateway
}

type service struct {
	name  string
	start func(ctx context.Context) error
	stop  func(ctx context.Context) error
}

// skipBinaries lists the runtime binaries a storage mode does not need.
func skipBinaries(cfg *config.Config) []string {
	if cfg.ProviderStorage() {
		return []string{"juicefs"}
	}
	return nil
}

// objectStore returns the S3 store, or nil in the provider mode without S3
// settings.
func objectStore(cfg *config.Config) (storage.ObjectStore, error) {
	if cfg.ProviderStorage() && !cfg.S3Configured() {
		return nil, nil
	}
	return storage.NewS3Store(cfg.S3, nil)
}

// newStorageRuntime builds the storage of the configured mode.
func newStorageRuntime(cfg *config.Config, driver string, boot *bootstrap.Bootstrapper, objects storage.ObjectStore,
	st *store.Store, log *slog.Logger) (*storageRuntime, error) {
	tid := cfg.TenantID
	if cfg.ProviderStorage() {
		b, err := storage.NewStateBackup(storage.StateBackupOptions{Config: cfg, Objects: objects, State: st, Logger: log})
		if err != nil {
			return nil, err
		}
		return &storageRuntime{
			Provider: b,
			Routes:   []api.RouteRegistrar{api.UnavailableGatewayRoutes{}},
			services: []service{{name: "storage", start: func(ctx context.Context) error { return b.Start(ctx, tid) },
				stop: func(ctx context.Context) error { return b.Stop(ctx, tid) }}},
		}, nil
	}
	juicefs, err := boot.Resolve("juicefs")
	if err != nil {
		return nil, err
	}
	runner := storage.OSRunner{}
	sup, err := storage.NewSupervisor(storage.Options{
		Config: cfg, JuiceFSBinary: juicefs, Runner: runner, Objects: objects, State: st,
		Logger: log, RequireRootUpper: driver == config.DriverCHV,
	})
	if err != nil {
		return nil, err
	}
	dav, err := storage.NewWebDAV(storage.WebDAVOptions{Config: cfg, JuiceFSBinary: juicefs, Runner: runner, Logger: log})
	if err != nil {
		return nil, err
	}
	s3gw, err := storage.NewS3Gateway(storage.S3GatewayOptions{Config: cfg, JuiceFSBinary: juicefs, Runner: runner, Logger: log})
	if err != nil {
		return nil, err
	}
	return &storageRuntime{
		Provider: sup,
		Files:    agentfs.NewLocal(sup, agentfs.LocalOptions{}),
		Routes:   []api.RouteRegistrar{api.WebDAVRoutes{Handler: dav.Handler()}, api.S3Routes{Handler: s3gw.Handler()}},
		Gateway:  s3gw,
		s3gw:     s3gw,
		services: []service{
			{name: "storage", start: func(ctx context.Context) error { return sup.Start(ctx, tid) },
				stop: func(ctx context.Context) error { return sup.Stop(ctx, tid) }},
			{name: "webdav", start: func(ctx context.Context) error { return dav.Start(ctx, tid) },
				stop: func(ctx context.Context) error { return dav.Stop(ctx, tid) }},
			{name: "s3 gateway", start: func(ctx context.Context) error { return s3gw.Start(ctx, tid) },
				stop: func(ctx context.Context) error { return s3gw.Stop(ctx, tid) }},
		},
	}, nil
}

// start starts the storage services and registers their shutdown steps.
func (s *storageRuntime) start(ctx context.Context, shutdown *shutdownStack) error {
	for _, svc := range s.services {
		if err := svc.start(ctx); err != nil {
			return err
		}
		shutdown.push(svc.name, svc.stop)
	}
	return nil
}

// printGateway prints the S3 gateway endpoint when it runs.
func (s *storageRuntime) printGateway(w io.Writer, addr fmt.Stringer) {
	if s.s3gw != nil && s.s3gw.Enabled() {
		fmt.Fprintf(w, "S3 gateway: endpoint http://%s%s, bucket %q (path style), access key %q, secret key = admin token\n",
			addr, storage.S3GatewayPrefix, storage.S3GatewayBucket, storage.S3GatewayAccessKey)
	}
}
