// Package remotefs implements agentfs.FS over the guest daemon file methods
// (internal/guest/rpc fs.*) for the provider storage mode, where the guest
// trees are on the machine disk that the host cannot mount.
//
// Every operation first takes a "files" lease on the VM controller and
// calls EnsureRunning, so a request wakes a paused or stopped machine and
// the machine does not hibernate while a transfer runs. A machine that the
// user stopped is not started: the operation fails with agentfs.ErrStopped.
// When the machine does not answer within WakeTimeout, the operation fails
// with agentfs.ErrStarting (HTTP 503 with Retry-After) while the start
// continues, or with agentfs.ErrUnavailable.
package remotefs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
)

// LeaseFiles is the VM lease kind held while a file operation runs.
const LeaseFiles = "files"

// DefaultWakeTimeout bounds the wait for the machine to answer. It is
// shorter than the 100 s after which a proxy in front of LydiaBox
// (Cloudflare) ends a request without a response; a longer start
// continues, and the client sends the request again (ErrStarting).
const DefaultWakeTimeout = 75 * time.Second

// Client is the guest daemon client (*guestclient.Client implements it).
type Client interface {
	FSRoots(ctx context.Context, tenantID string) (rpc.FSRootsResult, error)
	FSCall(ctx context.Context, tenantID, method string, params, out any) error
	FSOpen(ctx context.Context, tenantID string, p rpc.FSReadParams) (*guestclient.FSReader, error)
	FSWrite(ctx context.Context, tenantID string, p rpc.FSWriteParams, src io.Reader) (rpc.FSWriteResult, error)
}

// Waker boots or resumes the VM (vmm.Controller). For a VM that the user
// stopped it returns an error that wraps agentfs.ErrStopped.
type Waker interface {
	EnsureRunning(ctx context.Context, tenantID string) error
}

// Leaser keeps the VM awake (vmm.Controller).
type Leaser interface {
	AcquireLease(tenantID, kind string) (release func())
}

// Options configures an FS.
type Options struct {
	Client      Client
	Waker       Waker
	Leaser      Leaser
	WakeTimeout time.Duration
}

// FS is the guest-backed agentfs.FS.
type FS struct {
	opts Options

	mu    sync.Mutex
	roots map[string][]agentfs.RootInfo
}

// New returns an FS. Waker and Leaser may be nil (tests).
func New(opts Options) (*FS, error) {
	if opts.Client == nil {
		return nil, errors.New("remotefs: Client is required")
	}
	if opts.WakeTimeout <= 0 {
		opts.WakeTimeout = DefaultWakeTimeout
	}
	return &FS{opts: opts, roots: map[string][]agentfs.RootInfo{}}, nil
}

var _ agentfs.FS = (*FS)(nil)

// Begin takes a files lease and wakes the machine. The caller calls release
// when the operation (or transfer) ends.
func (f *FS) Begin(ctx context.Context, tenantID string) (release func(), err error) {
	release = func() {}
	if f.opts.Leaser != nil {
		release = f.opts.Leaser.AcquireLease(tenantID, LeaseFiles)
	}
	if f.opts.Waker == nil {
		return release, nil
	}
	wctx, cancel := context.WithTimeout(ctx, f.opts.WakeTimeout)
	defer cancel()
	if err := f.opts.Waker.EnsureRunning(wctx, tenantID); err != nil {
		release()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, agentfs.ErrStopped) {
			return nil, agentfs.ErrStopped
		}
		if errors.Is(err, agentfs.ErrStarting) {
			return nil, agentfs.ErrStarting
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, agentfs.Unavailable("the agent machine did not start within %s; retry the request", f.opts.WakeTimeout)
		}
		return nil, agentfs.Unavailable("wake the agent machine: %v", err)
	}
	return release, nil
}

// mapErr converts guest RPC errors to agentfs errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		if fe := agentfs.FromCode(re.Code, re.Message); fe != nil {
			return fe
		}
		switch re.Code {
		case rpc.CodeUnavailable, rpc.CodeNoMethod:
			return agentfs.Unavailable("%s", re.Message)
		case rpc.CodeBadRequest:
			return fmt.Errorf("%w: %s", agentfs.ErrInvalidPath, re.Message)
		}
		return errors.New(re.Message)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return agentfs.Unavailable("%v", err)
}

// call runs one unary method with a lease.
func (f *FS) call(ctx context.Context, tenantID, method string, params, out any) error {
	release, err := f.Begin(ctx, tenantID)
	if err != nil {
		return err
	}
	defer release()
	return mapErr(f.opts.Client.FSCall(ctx, tenantID, method, params, out))
}

// Roots implements agentfs.FS. It asks a running guest for its directories
// and otherwise returns the last answer or the defaults; it never wakes the
// machine.
func (f *FS) Roots(ctx context.Context, tenantID string) []agentfs.RootInfo {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if res, err := f.opts.Client.FSRoots(cctx, tenantID); err == nil && len(res.Roots) > 0 {
		out := make([]agentfs.RootInfo, 0, len(res.Roots))
		for _, r := range res.Roots {
			out = append(out, agentfs.RootInfo{Name: r.Name, GuestPath: r.GuestPath})
		}
		f.mu.Lock()
		f.roots[tenantID] = out
		f.mu.Unlock()
		return out
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.roots[tenantID]; r != nil {
		return r
	}
	return []agentfs.RootInfo{{Name: agentfs.RootWorkspace, GuestPath: agentfs.DefaultGuestWorkspace},
		{Name: agentfs.RootHome, GuestPath: agentfs.DefaultGuestHome}}
}

// precheck validates the root and path on the host, so invalid requests
// fail without waking the machine.
func precheck(root agentfs.Root, paths ...string) error {
	for _, p := range paths {
		if _, err := agentfs.CleanRel(p); err != nil {
			return err
		}
	}
	return agentfs.CheckRoot(root)
}

// Lstat implements agentfs.FS.
func (f *FS) Lstat(ctx context.Context, tenantID string, root agentfs.Root, p string) (agentfs.Entry, error) {
	if err := precheck(root, p); err != nil {
		return agentfs.Entry{}, err
	}
	var e rpc.FSEntry
	err := f.call(ctx, tenantID, rpc.MethodFSStat, rpc.FSPathParams{Root: root, Path: p}, &e)
	return agentfs.Entry(e), err
}

// List implements agentfs.FS.
func (f *FS) List(ctx context.Context, tenantID string, root agentfs.Root, p string) (agentfs.Entry, []agentfs.Entry, error) {
	if err := precheck(root, p); err != nil {
		return agentfs.Entry{}, nil, err
	}
	var res rpc.FSListResult
	if err := f.call(ctx, tenantID, rpc.MethodFSList, rpc.FSPathParams{Root: root, Path: p}, &res); err != nil {
		return agentfs.Entry{}, nil, err
	}
	out := make([]agentfs.Entry, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, agentfs.Entry(e))
	}
	return agentfs.Entry(res.Dir), out, nil
}

// Write implements agentfs.FS.
func (f *FS) Write(ctx context.Context, tenantID string, root agentfs.Root, p string, src io.Reader, o agentfs.WriteOptions) (agentfs.WriteResult, error) {
	if err := precheck(root, p); err != nil {
		return agentfs.WriteResult{}, err
	}
	release, err := f.Begin(ctx, tenantID)
	if err != nil {
		return agentfs.WriteResult{}, err
	}
	defer release()
	res, err := f.opts.Client.FSWrite(ctx, tenantID, rpc.FSWriteParams{
		Root: root, Path: p, Overwrite: o.Overwrite, MkdirParents: o.MkdirParents, IfModTime: o.IfModTime,
		Limit: o.Limit, Mode: uint32(o.Mode.Perm()), RegularOnly: o.RegularOnly,
	}, src)
	if err != nil {
		return agentfs.WriteResult{}, mapWriteErr(err)
	}
	return agentfs.WriteResult{Entry: agentfs.Entry(res.FSEntry), Replaced: res.Replaced}, nil
}

// mapWriteErr keeps errors of the upload source (for example an
// http.MaxBytesError) intact.
func mapWriteErr(err error) error {
	var re *rpc.Error
	if errors.As(err, &re) {
		return mapErr(err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, guestclient.ErrSourceFailed) {
		return err
	}
	return mapErr(err)
}

// Mkdir implements agentfs.FS.
func (f *FS) Mkdir(ctx context.Context, tenantID string, root agentfs.Root, p string, parents bool) (agentfs.Entry, error) {
	if err := precheck(root, p); err != nil {
		return agentfs.Entry{}, err
	}
	var e rpc.FSEntry
	err := f.call(ctx, tenantID, rpc.MethodFSMkdir, rpc.FSMkdirParams{Root: root, Path: p, Parents: parents}, &e)
	return agentfs.Entry(e), err
}

// Rename implements agentfs.FS.
func (f *FS) Rename(ctx context.Context, tenantID string, root agentfs.Root, from, to string, overwrite bool) (agentfs.Entry, error) {
	if err := precheck(root, from, to); err != nil {
		return agentfs.Entry{}, err
	}
	var e rpc.FSEntry
	err := f.call(ctx, tenantID, rpc.MethodFSRename, rpc.FSRenameParams{Root: root, From: from, To: to, Overwrite: overwrite}, &e)
	return agentfs.Entry(e), err
}

// Remove implements agentfs.FS.
func (f *FS) Remove(ctx context.Context, tenantID string, root agentfs.Root, p string, recursive bool) error {
	if err := precheck(root, p); err != nil {
		return err
	}
	return f.call(ctx, tenantID, rpc.MethodFSRemove, rpc.FSRemoveParams{Root: root, Path: p, Recursive: recursive}, nil)
}

// ReadFile implements agentfs.FS. Files above rpc.MaxFileData are
// ErrTooLarge.
func (f *FS) ReadFile(ctx context.Context, tenantID string, root agentfs.Root, p string) ([]byte, error) {
	if err := precheck(root, p); err != nil {
		return nil, err
	}
	var res rpc.FSFileData
	if err := f.call(ctx, tenantID, rpc.MethodFSReadFile, rpc.FSPathParams{Root: root, Path: p}, &res); err != nil {
		return nil, err
	}
	return res.Data, nil
}

// WriteFile implements agentfs.FS.
func (f *FS) WriteFile(ctx context.Context, tenantID string, root agentfs.Root, p string, data []byte, perm fs.FileMode) error {
	if err := precheck(root, p); err != nil {
		return err
	}
	if len(data) > rpc.MaxFileData {
		return fmt.Errorf("%w: %d bytes exceeds %d", agentfs.ErrTooLarge, len(data), rpc.MaxFileData)
	}
	return f.call(ctx, tenantID, rpc.MethodFSWriteFile, rpc.FSWriteFileParams{Root: root, Path: p, Data: data, Perm: uint32(perm.Perm())}, nil)
}

// MkdirAll implements agentfs.FS.
func (f *FS) MkdirAll(ctx context.Context, tenantID string, root agentfs.Root, p string) error {
	if err := precheck(root, p); err != nil {
		return err
	}
	return f.call(ctx, tenantID, rpc.MethodFSMkdirAll, rpc.FSPathParams{Root: root, Path: p}, nil)
}
