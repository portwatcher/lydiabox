package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
)

// ErrNoHostDirs is returned by StateBackup.HomeDir and WorkspaceDir: in the
// provider storage mode the guest trees are on the machine disk.
var ErrNoHostDirs = errors.New("storage: the guest directories are not on the host in the provider storage mode")

// StateBackupOptions configures a StateBackup.
type StateBackupOptions struct {
	Config *config.Config
	// Objects is the S3 store; nil disables the backup.
	Objects ObjectStore
	State   Snapshotter
	Logger  *slog.Logger
}

// StateBackup is the storage.Provider of the provider storage mode. The
// guest trees live on the machine disk, so there is no JuiceFS volume; when
// S3 settings are present it uploads snapshots of the host state database
// every MetaSyncInterval and at shutdown.
type StateBackup struct {
	opts   StateBackupOptions
	log    *slog.Logger
	tenant string

	syncMu  sync.Mutex
	up      *snapshotUploader
	stateMu sync.Mutex
	status  Status

	lifeMu sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewStateBackup returns the provider-mode storage service.
func NewStateBackup(opts StateBackupOptions) (*StateBackup, error) {
	if opts.Config == nil {
		return nil, errors.New("storage: Config is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	c := opts.Config
	b := &StateBackup{opts: opts, tenant: c.TenantID, log: opts.Logger.With("component", "storage", "tenant", c.TenantID),
		status: Status{TenantID: c.TenantID, Mode: config.StorageProvider}}
	if opts.Objects != nil {
		b.up = &snapshotUploader{objects: opts.Objects, tmpDir: c.TmpDir(), log: b.log, sums: map[string]string{}}
	}
	return b, nil
}

func (b *StateBackup) checkTenant(tenantID string) error {
	if tenantID != b.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	return nil
}

// HomeDir implements Provider; it always fails with ErrNoHostDirs.
func (b *StateBackup) HomeDir(tenantID string) (string, error) {
	if err := b.checkTenant(tenantID); err != nil {
		return "", err
	}
	return "", ErrNoHostDirs
}

// WorkspaceDir implements Provider; it always fails with ErrNoHostDirs.
func (b *StateBackup) WorkspaceDir(tenantID string) (string, error) {
	if err := b.checkTenant(tenantID); err != nil {
		return "", err
	}
	return "", ErrNoHostDirs
}

// Status implements Provider.
func (b *StateBackup) Status(tenantID string) Status {
	if tenantID != b.tenant {
		return Status{TenantID: tenantID, Mode: config.StorageProvider}
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.status
}

// Start starts the backup ticker, or logs one warning when no S3 settings
// are present.
func (b *StateBackup) Start(ctx context.Context, tenantID string) error {
	if err := b.checkTenant(tenantID); err != nil {
		return err
	}
	if b.up == nil || b.opts.State == nil {
		b.log.Warn("storage mode provider without S3 settings: the host state database (sessions, bindings, " +
			"connector tokens) is not backed up; set LYDIABOX_S3_* to back it up")
		return nil
	}
	b.lifeMu.Lock()
	defer b.lifeMu.Unlock()
	if b.cancel != nil {
		return errors.New("storage: already started")
	}
	sctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b.cancel, b.done = cancel, make(chan struct{})
	go b.loop(sctx)
	return nil
}

func (b *StateBackup) loop(ctx context.Context) {
	defer close(b.done)
	t := time.NewTicker(b.opts.Config.MetaSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := b.SyncNow(ctx, b.tenant); err != nil && ctx.Err() == nil {
				b.log.Error("state database backup failed", "err", err)
			}
		}
	}
}

// SyncNow uploads a state database snapshot when it changed. Without S3
// settings it does nothing.
func (b *StateBackup) SyncNow(ctx context.Context, tenantID string) error {
	if err := b.checkTenant(tenantID); err != nil {
		return err
	}
	if b.up == nil || b.opts.State == nil {
		return nil
	}
	b.syncMu.Lock()
	defer b.syncMu.Unlock()
	uploaded, err := b.up.upload(ctx, StateBackupKey(b.tenant), stateSnapshot(ctx, b.opts.State, b.opts.Config.TmpDir()))
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if err != nil {
		b.status.LastSyncError = err.Error()
		return fmt.Errorf("storage: state database backup: %w", err)
	}
	now := time.Now().UTC()
	b.status.LastSyncAt, b.status.LastSyncError = now, ""
	if uploaded {
		b.status.LastUploadAt = now
	}
	return nil
}

// Stop ends the ticker and uploads a final snapshot.
func (b *StateBackup) Stop(ctx context.Context, tenantID string) error {
	if err := b.checkTenant(tenantID); err != nil {
		return err
	}
	b.lifeMu.Lock()
	cancel, done := b.cancel, b.done
	b.cancel = nil
	b.lifeMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	<-done
	return b.SyncNow(ctx, tenantID)
}
