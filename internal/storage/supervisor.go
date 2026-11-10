package storage

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
)

// Provider is the tenant-scoped persistent storage service consumed by the
// API server and later phases (vmm mounts HomeDir/WorkspaceDir via virtiofsd,
// harness reads session files from HomeDir).
type Provider interface {
	Start(ctx context.Context, tenantID string) error
	Stop(ctx context.Context, tenantID string) error
	Status(tenantID string) Status
	SyncNow(ctx context.Context, tenantID string) error
	HomeDir(tenantID string) (string, error)
	WorkspaceDir(tenantID string) (string, error)
}

// Status is the storage state reported by GET /api/v1/status.
type Status struct {
	TenantID      string    `json:"tenantId"`
	Mounted       bool      `json:"mounted"`
	LastSyncAt    time.Time `json:"lastSyncAt"`
	LastUploadAt  time.Time `json:"lastUploadAt"`
	LastSyncError string    `json:"lastSyncError,omitempty"`
}

// Snapshotter produces a consistent copy of the host state database.
type Snapshotter interface {
	VacuumInto(ctx context.Context, dest string) error
}

// ErrUnknownTenant is returned for a tenant this supervisor does not serve.
var ErrUnknownTenant = errors.New("storage: unknown tenant")

// DefaultRootUpperSize is the sparse size of system/root-upper.img (40 GiB).
const DefaultRootUpperSize int64 = 40 << 30

// Options configures a Supervisor.
type Options struct {
	Config *config.Config
	// JuiceFSBinary is the juicefs executable (from bootstrap).
	JuiceFSBinary string
	// MkfsBinary formats root-upper.img; defaults to "mkfs.ext4" on PATH.
	MkfsBinary string
	Runner     Runner
	Objects    ObjectStore
	// State is snapshotted next to the JuiceFS metadata; nil skips it.
	State  Snapshotter
	Logger *slog.Logger
	// MountProbe reports whether dir is a live JuiceFS mount. The default
	// checks for the virtual .config file that JuiceFS exposes at the root.
	MountProbe func(dir string) bool
	// RequireRootUpper fails Start when root-upper.img cannot be created
	// (the chv driver needs it; the docker driver tolerates its absence).
	RequireRootUpper bool
	RootUpperSize    int64
	MountTimeout     time.Duration
	// Chown sets uid/gid 1000 on provisioned directories when running as root.
	Chown func(path string, uid, gid int) error
}

// Supervisor runs the JuiceFS volume of one tenant: metadata recovery or
// format, the supervised mount, directory provisioning, and metadata backups.
type Supervisor struct {
	opts    Options
	cfg     *config.Config
	jfs     JuiceFS
	log     *slog.Logger
	tenant  string
	syncMu  sync.Mutex
	stateMu sync.Mutex
	status  Status
	sums    map[string]string

	lifeMu    sync.Mutex
	started   bool
	stopping  chan struct{}
	mountDone chan struct{}
	syncDone  chan struct{}
	syncStop  context.CancelFunc
	proc      Process
	watch     *procWatch
}

// procWatch records the exit of a process; done is closed after Wait returns,
// so any number of goroutines can observe the exit.
type procWatch struct {
	done chan struct{}
	err  error
}

func watchProcess(p Process) *procWatch {
	w := &procWatch{done: make(chan struct{})}
	go func() {
		w.err = p.Wait()
		close(w.done)
	}()
	return w
}

// NewSupervisor validates options.
func NewSupervisor(opts Options) (*Supervisor, error) {
	if opts.Config == nil || opts.Runner == nil || opts.Objects == nil || opts.JuiceFSBinary == "" {
		return nil, errors.New("storage: Config, Runner, Objects, and JuiceFSBinary are required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MkfsBinary == "" {
		opts.MkfsBinary = "mkfs.ext4"
	}
	if opts.MountProbe == nil {
		opts.MountProbe = DefaultMountProbe
	}
	if opts.RootUpperSize <= 0 {
		opts.RootUpperSize = DefaultRootUpperSize
	}
	if opts.MountTimeout <= 0 {
		opts.MountTimeout = 90 * time.Second
	}
	if opts.Chown == nil {
		opts.Chown = os.Lchown
	}
	c := opts.Config
	return &Supervisor{
		opts:   opts,
		cfg:    c,
		tenant: c.TenantID,
		log:    opts.Logger.With("component", "storage", "tenant", c.TenantID),
		jfs:    JuiceFS{Binary: opts.JuiceFSBinary, MetaURL: c.JuiceFSMetaURL(), MountDir: c.MountDir(), CacheDir: c.CacheDir()},
		status: Status{TenantID: c.TenantID},
		sums:   map[string]string{},
	}, nil
}

// DefaultMountProbe reports whether dir holds the JuiceFS virtual .config file.
func DefaultMountProbe(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".config"))
	return err == nil
}

func (s *Supervisor) checkTenant(tenantID string) error {
	if tenantID != s.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	return nil
}

// HomeDir returns the host path of the guest /home/agent.
func (s *Supervisor) HomeDir(tenantID string) (string, error) {
	if err := s.checkTenant(tenantID); err != nil {
		return "", err
	}
	return s.cfg.HomeDir(), nil
}

// WorkspaceDir returns the host path of the guest /workspace.
func (s *Supervisor) WorkspaceDir(tenantID string) (string, error) {
	if err := s.checkTenant(tenantID); err != nil {
		return "", err
	}
	return s.cfg.WorkspaceDir(), nil
}

// Status returns a snapshot of the storage state.
func (s *Supervisor) Status(tenantID string) Status {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	st := s.status
	st.TenantID = tenantID
	if tenantID != s.tenant {
		return Status{TenantID: tenantID}
	}
	return st
}

func (s *Supervisor) setMounted(v bool) {
	s.stateMu.Lock()
	s.status.Mounted = v
	s.stateMu.Unlock()
}

// Start prepares and mounts the volume, provisions the guest directories, and
// starts the metadata backup ticker.
func (s *Supervisor) Start(ctx context.Context, tenantID string) error {
	if err := s.checkTenant(tenantID); err != nil {
		return err
	}
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.started {
		return errors.New("storage: already started")
	}
	for _, d := range []string{s.cfg.MetaDir(), s.cfg.MountDir(), s.cfg.CacheDir(), s.cfg.TmpDir(), s.cfg.RunDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("storage: create %s: %w", d, err)
		}
	}
	if err := s.prepareVolume(ctx); err != nil {
		return err
	}
	if s.opts.MountProbe(s.cfg.MountDir()) {
		s.log.Warn("stale JuiceFS mount found; unmounting before start")
		_ = s.opts.Runner.Run(ctx, s.jfs.Umount(true))
	}
	proc, watch, err := s.launchMount(ctx)
	if err != nil {
		return err
	}
	if err := s.provision(ctx); err != nil {
		s.terminate(proc, watch)
		return err
	}
	s.proc, s.watch = proc, watch
	s.stopping = make(chan struct{})
	s.mountDone = make(chan struct{})
	s.syncDone = make(chan struct{})
	syncCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.syncStop = cancel
	s.started = true
	go s.superviseMount()
	go s.syncLoop(syncCtx)
	return nil
}

// prepareVolume restores metadata from S3 or formats a new volume when the
// local metadata database is missing.
func (s *Supervisor) prepareVolume(ctx context.Context) error {
	metaPath := s.cfg.JuiceFSMetaPath()
	if _, err := os.Stat(metaPath); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: stat %s: %w", metaPath, err)
	}
	creds, err := s.credentials()
	if err != nil {
		return err
	}
	key := MetaBackupKey(s.tenant)
	body, err := s.opts.Objects.Get(ctx, key)
	switch {
	case err == nil:
		defer body.Close()
		s.log.Info("restoring JuiceFS metadata from backup", "key", key)
		gz, err := gzip.NewReader(body)
		if err != nil {
			return fmt.Errorf("storage: open metadata backup: %w", err)
		}
		load := s.jfs.Load()
		load.Stdin = gz
		if err := s.opts.Runner.Run(ctx, load); err != nil {
			removeMeta(metaPath)
			return fmt.Errorf("storage: juicefs load: %w", err)
		}
		if err := s.opts.Runner.Run(ctx, s.jfs.RestoreCredentials(creds)); err != nil {
			removeMeta(metaPath)
			return fmt.Errorf("storage: juicefs config: %w", err)
		}
		return nil
	case errors.Is(err, ErrObjectNotFound):
		s.log.Info("formatting new JuiceFS volume", "volume", VolumeName(s.tenant))
		if err := s.opts.Runner.Run(ctx, s.jfs.Format(s.tenant, creds)); err != nil {
			removeMeta(metaPath)
			return fmt.Errorf("storage: juicefs format: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("storage: check metadata backup: %w", err)
	}
}

func (s *Supervisor) credentials() (S3Credentials, error) {
	u, err := s.cfg.S3.ResolvedBucketURL()
	if err != nil {
		return S3Credentials{}, err
	}
	return S3Credentials{BucketURL: u, AccessKey: s.cfg.S3.AccessKey, SecretKey: s.cfg.S3.SecretKey}, nil
}

func removeMeta(path string) {
	for _, p := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		os.Remove(p)
	}
}

// launchMount starts `juicefs mount` and waits until the probe succeeds.
func (s *Supervisor) launchMount(ctx context.Context) (Process, *procWatch, error) {
	cmd := s.jfs.Mount()
	out := newLogWriter(s.log, "juicefs mount")
	cmd.Stdout, cmd.Stderr = out, out
	proc, err := s.opts.Runner.Start(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: %w", err)
	}
	watch := watchProcess(proc)

	deadline := time.NewTimer(s.opts.MountTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.opts.MountProbe(s.cfg.MountDir()) {
			s.setMounted(true)
			s.log.Info("JuiceFS mounted", "dir", s.cfg.MountDir(), "pid", proc.Pid())
			return proc, watch, nil
		}
		select {
		case <-watch.done:
			return nil, nil, fmt.Errorf("storage: juicefs mount exited before ready: %v", watch.err)
		case <-deadline.C:
			s.terminate(proc, watch)
			return nil, nil, fmt.Errorf("storage: juicefs mount not ready after %s", s.opts.MountTimeout)
		case <-ctx.Done():
			s.terminate(proc, watch)
			return nil, nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// terminate stops a mount process: SIGTERM, then SIGKILL after 30 seconds.
func (s *Supervisor) terminate(proc Process, w *procWatch) {
	_ = proc.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
	case <-time.After(30 * time.Second):
		_ = proc.Kill()
		<-w.done
	}
	s.setMounted(false)
}

// superviseMount restarts the mount with exponential backoff when it exits
// unexpectedly.
func (s *Supervisor) superviseMount() {
	defer close(s.mountDone)
	backoff := time.Second
	for {
		s.lifeMu.Lock()
		w := s.watch
		s.lifeMu.Unlock()
		select {
		case <-w.done:
		case <-s.stopping:
			return
		}
		s.setMounted(false)
		select {
		case <-s.stopping:
			return
		default:
		}
		s.log.Error("juicefs mount exited; restarting", "err", w.err, "backoff", backoff)
		for {
			select {
			case <-s.stopping:
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), s.opts.MountTimeout+30*time.Second)
			go func() {
				select {
				case <-s.stopping:
					cancel()
				case <-ctx.Done():
				}
			}()
			_ = s.opts.Runner.Run(ctx, s.jfs.Umount(true))
			proc, newWatch, lerr := s.launchMount(ctx)
			cancel()
			if lerr != nil {
				s.log.Error("juicefs remount failed", "err", lerr)
				continue
			}
			s.lifeMu.Lock()
			s.proc, s.watch = proc, newWatch
			s.lifeMu.Unlock()
			backoff = time.Second
			break
		}
	}
}

// Stop unmounts with a writeback flush, waits for the mount process, and
// uploads a final metadata snapshot.
func (s *Supervisor) Stop(ctx context.Context, tenantID string) error {
	if err := s.checkTenant(tenantID); err != nil {
		return err
	}
	s.lifeMu.Lock()
	if !s.started {
		s.lifeMu.Unlock()
		return nil
	}
	s.started = false
	close(s.stopping)
	s.syncStop()
	s.lifeMu.Unlock()

	<-s.syncDone
	<-s.mountDone

	var errs []error
	if err := s.opts.Runner.Run(ctx, s.jfs.Umount(false)); err != nil {
		s.log.Warn("juicefs umount --flush failed; terminating mount process", "err", err)
	}
	s.lifeMu.Lock()
	proc, watch := s.proc, s.watch
	s.lifeMu.Unlock()
	if proc != nil {
		select {
		case <-watch.done:
			s.setMounted(false)
		case <-time.After(5 * time.Second):
			s.terminate(proc, watch)
		}
	}
	if err := s.SyncNow(ctx, tenantID); err != nil {
		errs = append(errs, fmt.Errorf("storage: final metadata sync: %w", err))
	}
	return errors.Join(errs...)
}
