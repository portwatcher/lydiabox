package guestclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// The file and session methods of the provider storage mode. They never
// wake the VM: internal/agentfs/remotefs takes a lease and wakes it first.

// FSRoots returns the guest roots.
func (c *Client) FSRoots(ctx context.Context, tenantID string) (rpc.FSRootsResult, error) {
	var res rpc.FSRootsResult
	err := c.call(ctx, tenantID, false, rpc.MethodFSRoots, nil, &res)
	return res, err
}

// FSCall performs a unary fs.* or harness.* method.
func (c *Client) FSCall(ctx context.Context, tenantID, method string, params, out any) error {
	return c.call(ctx, tenantID, false, method, params, out)
}

// FSReader streams a guest file (fs.read).
type FSReader struct {
	st    *rpc.Stream
	Entry rpc.FSEntry

	pending []byte
	done    bool
	stop    func() bool
}

// FSOpen opens a guest file for reading from offset. Canceling ctx closes
// the stream.
func (c *Client) FSOpen(ctx context.Context, tenantID string, p rpc.FSReadParams) (*FSReader, error) {
	var out *FSReader
	err := c.do(ctx, tenantID, false, func(s *rpc.Session) error {
		st, raw, err := s.Open(ctx, rpc.MethodFSRead, p)
		if err != nil {
			return err
		}
		r := &FSReader{st: st}
		if err := json.Unmarshal(raw, &r.Entry); err != nil {
			st.Close()
			return fmt.Errorf("guestclient: decode fs.read result: %w", err)
		}
		r.stop = context.AfterFunc(ctx, func() { st.Close() })
		out = r
		return nil
	})
	return out, err
}

// Read implements io.Reader. A failed guest read returns the guest error.
func (r *FSReader) Read(b []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		typ, payload, err := r.st.ReadFrame()
		if err != nil {
			return 0, err
		}
		switch typ {
		case rpc.FrameData:
			r.pending = payload
		case rpc.FrameJSON:
			var end rpc.FSStreamEnd
			if err := json.Unmarshal(payload, &end); err != nil {
				return 0, fmt.Errorf("guestclient: decode fs.read end: %w", err)
			}
			r.done = true
			if end.Error != nil {
				return 0, end.Error
			}
		}
	}
	n := copy(b, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// Close ends the stream.
func (r *FSReader) Close() error {
	r.stop()
	return r.st.Close()
}

// FSWrite streams src into an atomic guest write (fs.write) and returns the
// committed result. An error reading src aborts the write; the guest then
// removes its temporary file.
func (c *Client) FSWrite(ctx context.Context, tenantID string, p rpc.FSWriteParams, src io.Reader) (rpc.FSWriteResult, error) {
	var st *rpc.Stream
	err := c.do(ctx, tenantID, false, func(s *rpc.Session) error {
		var err error
		st, _, err = s.Open(ctx, rpc.MethodFSWrite, p)
		return err
	})
	if err != nil {
		return rpc.FSWriteResult{}, err
	}
	stop := context.AfterFunc(ctx, func() { st.Close() })
	defer stop()

	sendErr := make(chan error, 1)
	go func() { sendErr <- sendContent(st, src) }()

	var resp rpc.Response
	rerr := st.ReadJSON(&resp)
	// Closing the stream stops a sender that still writes after an early
	// guest answer; wait for it so src is no longer read on return.
	st.Close()
	serr := <-sendErr
	if rerr != nil {
		// The stream closed without a result: the sender failed (and
		// closed it) or the connection broke.
		if serr != nil {
			return rpc.FSWriteResult{}, serr
		}
		if ctx.Err() != nil {
			return rpc.FSWriteResult{}, ctx.Err()
		}
		return rpc.FSWriteResult{}, fmt.Errorf("guestclient: fs.write: %w", rerr)
	}
	if resp.Error != nil {
		return rpc.FSWriteResult{}, resp.Error
	}
	var res rpc.FSWriteResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return rpc.FSWriteResult{}, fmt.Errorf("guestclient: decode fs.write result: %w", err)
	}
	return res, nil
}

// ErrSourceFailed marks a failure to read the content of a write.
var ErrSourceFailed = errors.New("read upload content")

func sendContent(st *rpc.Stream, src io.Reader) error {
	buf := make([]byte, 256<<10)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if werr := st.WriteFrame(rpc.FrameData, buf[:n]); werr != nil {
				// The guest answered early (an error) and closed the stream.
				return nil
			}
			total += int64(n)
		}
		if errors.Is(err, io.EOF) {
			_ = st.WriteJSON(rpc.FSWriteCommit{Size: total})
			return nil
		}
		if err != nil {
			st.Close()
			return fmt.Errorf("%w: %w", ErrSourceFailed, err)
		}
	}
}
