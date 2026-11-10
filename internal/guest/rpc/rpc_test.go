package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAddr(t *testing.T) {
	cases := map[string]Addr{
		"vsock://2:9001":       {Network: "vsock", CID: 2, Port: 9001},
		"vsock://:9000":        {Network: "vsock", CID: AnyCID, Port: 9000},
		"unix:///run/x/g.sock": {Network: "unix", Path: "/run/x/g.sock"},
	}
	for in, want := range cases {
		got, err := ParseAddr(in)
		if err != nil || got != want {
			t.Errorf("ParseAddr(%q) = %+v, %v", in, got, err)
		}
		if got.String() != in {
			t.Errorf("String() = %q, want %q", got.String(), in)
		}
	}
	for _, bad := range []string{"tcp://1.2.3.4:5", "unix://relative", "vsock://2", "vsock://x:1", "vsock://2:port"} {
		if _, err := ParseAddr(bad); err == nil {
			t.Errorf("ParseAddr(%q) accepted", bad)
		}
	}
	if _, err := Dial(context.Background(), "vsock://:9000"); err == nil {
		t.Error("dial without cid accepted")
	}
}

func TestFramesAndUnaryCall(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "g.sock")
	ln, err := Listen("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o666 {
		t.Fatalf("socket mode %v %v", fi, err)
	}
	srv := NewServer(nil)
	srv.HandleUnary("echo", func(_ context.Context, p json.RawMessage) (any, error) { return p, nil })
	srv.HandleUnary("fail", func(context.Context, json.RawMessage) (any, error) {
		return nil, Errorf(CodeNotFound, "nothing here")
	})
	srv.HandleUnary("block", func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	srv.Handle("stream", func(_ context.Context, st *Stream, _ json.RawMessage) error {
		if err := st.Reply(nil); err != nil {
			return err
		}
		big := bytes.Repeat([]byte("x"), MaxFrame+10)
		return st.WriteData(big)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	conn, err := Dial(ctx, "unix://"+sock)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSession(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out map[string]int
	if err := s.Call(ctx, "echo", map[string]int{"a": 1}, &out); err != nil || out["a"] != 1 {
		t.Fatalf("echo %v %v", out, err)
	}
	if err := s.Call(ctx, "fail", nil, nil); !IsCode(err, CodeNotFound) {
		t.Fatalf("fail: %v", err)
	}
	if err := s.Call(ctx, "missing", nil, nil); !IsCode(err, CodeNoMethod) {
		t.Fatalf("missing: %v", err)
	}
	cctx, ccancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer ccancel()
	if err := s.Call(cctx, "block", nil, nil); err != context.DeadlineExceeded {
		t.Fatalf("block: %v", err)
	}
	st, _, err := s.Open(ctx, "stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for total < MaxFrame+10 {
		typ, b, err := st.ReadFrame()
		if err != nil || typ != FrameData {
			t.Fatalf("frame %q %v", typ, err)
		}
		total += len(b)
	}
	st.Close()

	// Stale socket files are replaced; other files are refused.
	ln2, err := ListenUnix(filepath.Join(dir, "g2.sock"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ln2.(*net.UnixListener).SetUnlinkOnClose(false)
	ln2.Close()
	if ln3, err := ListenUnix(filepath.Join(dir, "g2.sock"), 0o600); err != nil {
		t.Fatalf("stale socket: %v", err)
	} else {
		ln3.Close()
	}
	plain := filepath.Join(dir, "file")
	_ = os.WriteFile(plain, nil, 0o600)
	if _, err := ListenUnix(plain, 0o600); err == nil {
		t.Fatal("regular file replaced")
	}
}
