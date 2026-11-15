//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/guest/guestfs"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// readChunk is the size of the FrameData chunks of fs.read.
const readChunk = 256 << 10

// fsService serves the fs.* and harness.* methods. err is set when the file
// service could not be configured; every method then answers with it.
type fsService struct {
	files *guestfs.Service
	err   error
}

// filesFromEnv builds the file service from the guest environment.
func filesFromEnv() (*guestfs.Service, error) {
	opts, err := guestfs.FromEnv()
	if err != nil {
		return nil, err
	}
	return guestfs.New(opts)
}

func registerFS(srv *rpc.Server, files *guestfs.Service) {
	f := &fsService{files: files}
	if files == nil {
		f.files, f.err = filesFromEnv()
	}
	srv.HandleUnary(rpc.MethodFSRoots, f.roots)
	srv.HandleUnary(rpc.MethodFSStat, f.stat)
	srv.HandleUnary(rpc.MethodFSList, f.list)
	srv.Handle(rpc.MethodFSRead, f.read)
	srv.Handle(rpc.MethodFSWrite, f.write)
	srv.HandleUnary(rpc.MethodFSMkdir, f.mkdir)
	srv.HandleUnary(rpc.MethodFSRename, f.rename)
	srv.HandleUnary(rpc.MethodFSRemove, f.remove)
	srv.HandleUnary(rpc.MethodFSReadFile, f.readFile)
	srv.HandleUnary(rpc.MethodFSWriteFile, f.writeFile)
	srv.HandleUnary(rpc.MethodFSMkdirAll, f.mkdirAll)
	srv.HandleUnary(rpc.MethodHarnessDiscover, f.discover)
	srv.HandleUnary(rpc.MethodHarnessPurge, f.purge)
}

// fsErr converts an FS error to an rpc.Error with its agentfs code.
func fsErr(err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return err
	}
	if code := agentfs.Code(err); code != "" {
		return rpc.Errorf(code, "%s", err.Error())
	}
	return rpc.Errorf(rpc.CodeInternal, "%s", err.Error())
}

func (f *fsService) ready() error {
	if f.err != nil {
		return rpc.Errorf(rpc.CodeUnavailable, "guest file service: %v", f.err)
	}
	return nil
}

func entry(e agentfs.Entry) rpc.FSEntry { return rpc.FSEntry(e) }

func (f *fsService) roots(context.Context, json.RawMessage) (any, error) {
	if err := f.ready(); err != nil {
		return nil, err
	}
	var res rpc.FSRootsResult
	for _, r := range f.files.Roots() {
		res.Roots = append(res.Roots, rpc.FSRoot{Name: r.Name, GuestPath: r.GuestPath})
	}
	return res, nil
}

func (f *fsService) stat(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSPathParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	e, err := f.files.Lstat(p.Root, p.Path)
	return entry(e), fsErr(err)
}

func (f *fsService) list(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSPathParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	self, entries, err := f.files.List(p.Root, p.Path)
	if err != nil {
		return nil, fsErr(err)
	}
	res := rpc.FSListResult{Dir: entry(self), Entries: make([]rpc.FSEntry, 0, len(entries))}
	for _, e := range entries {
		res.Entries = append(res.Entries, entry(e))
	}
	return res, nil
}

func (f *fsService) decode(params json.RawMessage, v any) error {
	if err := f.ready(); err != nil {
		return err
	}
	return decode(params, v)
}

// read streams a file from the requested offset.
func (f *fsService) read(_ context.Context, st *rpc.Stream, params json.RawMessage) error {
	var p rpc.FSReadParams
	if err := f.decode(params, &p); err != nil {
		return st.ReplyError(err)
	}
	file, e, err := f.files.Open(p.Root, p.Path)
	if err != nil {
		return st.ReplyError(fsErr(err))
	}
	defer file.Close()
	if p.Offset < 0 || p.Offset > e.Size {
		return st.ReplyError(rpc.Errorf(agentfs.CodeInvalidPath, "offset %d is outside the file (%d bytes)", p.Offset, e.Size))
	}
	if _, err := file.Seek(p.Offset, io.SeekStart); err != nil {
		return st.ReplyError(fsErr(err))
	}
	if err := st.Reply(entry(e)); err != nil {
		return err
	}
	buf := make([]byte, readChunk)
	for {
		n, rerr := file.Read(buf)
		if n > 0 {
			if err := st.WriteFrame(rpc.FrameData, buf[:n]); err != nil {
				return err
			}
		}
		if errors.Is(rerr, io.EOF) {
			return st.WriteJSON(rpc.FSStreamEnd{})
		}
		if rerr != nil {
			var re *rpc.Error
			errors.As(fsErr(rerr), &re)
			return st.WriteJSON(rpc.FSStreamEnd{Error: re})
		}
	}
}

// errAborted ends a write whose client closed the stream before the commit.
var errAborted = errors.New("the client aborted the write")

// write receives the content of an atomic write and commits it when the
// client sends FSWriteCommit.
func (f *fsService) write(_ context.Context, st *rpc.Stream, params json.RawMessage) error {
	var p rpc.FSWriteParams
	if err := f.decode(params, &p); err != nil {
		return st.ReplyError(err)
	}
	if _, err := agentfs.CleanRel(p.Path); err != nil {
		return st.ReplyError(fsErr(err))
	}
	if err := agentfs.CheckRoot(p.Root); err != nil {
		return st.ReplyError(fsErr(err))
	}
	if err := st.Reply(nil); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	go func() {
		var got int64
		for {
			typ, b, err := st.ReadFrame()
			if err != nil {
				pw.CloseWithError(errAborted)
				return
			}
			switch typ {
			case rpc.FrameData:
				got += int64(len(b))
				if _, err := pw.Write(b); err != nil {
					return
				}
			case rpc.FrameJSON:
				var c rpc.FSWriteCommit
				if json.Unmarshal(b, &c) != nil || c.Size != got {
					pw.CloseWithError(fmt.Errorf("%w: the write ended after %d of %d bytes", agentfs.ErrInvalidPath, got, c.Size))
					return
				}
				pw.Close()
				return
			}
		}
	}()
	res, err := f.files.Write(p.Root, p.Path, pr, agentfs.WriteOptions{
		Overwrite: p.Overwrite, MkdirParents: p.MkdirParents, IfModTime: p.IfModTime, Limit: p.Limit,
		Mode: fs.FileMode(p.Mode).Perm(), RegularOnly: p.RegularOnly,
	})
	pr.CloseWithError(errAborted)
	if err != nil {
		if errors.Is(err, errAborted) {
			return nil
		}
		return st.ReplyError(fsErr(err))
	}
	return st.Reply(rpc.FSWriteResult{FSEntry: entry(res.Entry), Replaced: res.Replaced})
}

func (f *fsService) mkdir(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSMkdirParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	e, err := f.files.Mkdir(p.Root, p.Path, p.Parents)
	return entry(e), fsErr(err)
}

func (f *fsService) rename(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSRenameParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	e, err := f.files.Rename(p.Root, p.From, p.To, p.Overwrite)
	return entry(e), fsErr(err)
}

func (f *fsService) remove(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSRemoveParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	return nil, fsErr(f.files.Remove(p.Root, p.Path, p.Recursive))
}

func (f *fsService) readFile(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSPathParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	data, err := f.files.ReadFile(p.Root, p.Path, rpc.MaxFileData)
	if err != nil {
		return nil, fsErr(err)
	}
	return rpc.FSFileData{Data: data}, nil
}

func (f *fsService) writeFile(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSWriteFileParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	return nil, fsErr(f.files.WriteFile(p.Root, p.Path, p.Data, fs.FileMode(p.Perm).Perm()))
}

func (f *fsService) mkdirAll(_ context.Context, params json.RawMessage) (any, error) {
	var p rpc.FSPathParams
	if err := f.decode(params, &p); err != nil {
		return nil, err
	}
	return nil, fsErr(f.files.MkdirAll(p.Root, p.Path))
}
