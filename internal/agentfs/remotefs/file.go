package remotefs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
)

// Open implements agentfs.FS. The returned file holds the files lease until
// it is closed. Seek is lazy: the next Read reopens the guest stream at the
// new offset, so http.ServeContent can serve Range requests.
func (f *FS) Open(ctx context.Context, tenantID string, root agentfs.Root, p string) (agentfs.File, error) {
	if err := precheck(root, p); err != nil {
		return nil, err
	}
	release, err := f.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	params := rpc.FSReadParams{Root: root, Path: p}
	r, err := f.opts.Client.FSOpen(ctx, tenantID, params)
	if err != nil {
		release()
		return nil, mapErr(err)
	}
	return &file{ctx: ctx, client: f.opts.Client, tenantID: tenantID, params: params, r: r,
		info: agentfs.Entry(r.Entry), release: release}, nil
}

type file struct {
	ctx      context.Context
	client   Client
	tenantID string
	params   rpc.FSReadParams
	info     agentfs.Entry
	release  func()

	r      *guestclient.FSReader
	rOff   int64 // offset of the next byte of r
	off    int64 // offset of the next Read
	closed bool
}

func (f *file) Info() agentfs.Entry { return f.info }

func (f *file) Read(b []byte) (int, error) {
	if f.closed {
		return 0, errors.New("remotefs: read of a closed file")
	}
	if f.off >= f.info.Size {
		return 0, io.EOF
	}
	if f.r == nil || f.rOff != f.off {
		if f.r != nil {
			f.r.Close()
			f.r = nil
		}
		p := f.params
		p.Offset = f.off
		r, err := f.client.FSOpen(f.ctx, f.tenantID, p)
		if err != nil {
			return 0, mapErr(err)
		}
		f.r, f.rOff = r, f.off
	}
	n, err := f.r.Read(b)
	f.rOff += int64(n)
	f.off += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		err = mapErr(err)
	}
	return n, err
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.off + offset
	case io.SeekEnd:
		abs = f.info.Size + offset
	default:
		return 0, fmt.Errorf("remotefs: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("remotefs: negative offset")
	}
	f.off = abs
	return abs, nil
}

func (f *file) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.r != nil {
		f.r.Close()
	}
	f.release()
	return nil
}
