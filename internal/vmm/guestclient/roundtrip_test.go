//go:build unix

package guestclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/mcp"
	"github.com/lydiabox/lydiabox/internal/guest/ptymanager"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/rpc/daemon"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

type unixDialer struct{ path string }

func (d unixDialer) DialGuest(ctx context.Context, _ string, port uint32) (net.Conn, error) {
	if port != rpc.PortGuestRPC {
		return nil, io.ErrUnexpectedEOF
	}
	return rpc.Dial(ctx, "unix://"+d.path)
}

type leaser struct {
	mu     sync.Mutex
	active map[string]int
}

func (l *leaser) AcquireLease(tenantID, kind string) func() {
	l.mu.Lock()
	l.active[kind]++
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.active[kind]--
			l.mu.Unlock()
		})
	}
}

func (l *leaser) count(kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active[kind]
}

type noApps struct{}

func (noApps) Launch(context.Context, string) error { return nil }
func (noApps) CDPEndpoint(context.Context) (string, error) {
	return "ws://127.0.0.1:9222/devtools/browser/x", nil
}
func (noApps) RunBlender(context.Context, mcp.BlenderRequest) (mcp.BlenderResult, error) {
	return mcp.BlenderResult{}, nil
}

func startGuest(t *testing.T) (*Client, *leaser, *x11.Fake) {
	t.Helper()
	dir, err := os.MkdirTemp("", "lbxg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "guest.sock")
	ln, err := rpc.Listen("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	ptys := ptymanager.New(ptymanager.Options{Logger: log, Shell: "/bin/sh", DefaultDir: dir, Env: ptymanager.DefaultEnv(rpc.HostAddrDocker)})
	fake := x11.NewFake(1920, 1080)
	desk := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return fake, nil }, TakeoverWait: 20 * time.Millisecond})
	srv := rpc.NewServer(log)
	daemon.Register(srv, daemon.Options{PTY: ptys, Desktop: desk, MCP: mcp.NewServer(desk, noApps{}), Apps: noApps{}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		ptys.Close(cctx)
	})
	l := &leaser{active: map[string]int{}}
	return New(Options{Dialer: unixDialer{sock}, Leaser: l, Logger: log}), l, fake
}

func readUntil(t *testing.T, p PTY, want string) string {
	t.Helper()
	var buf bytes.Buffer
	b := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %q; got %q", want, buf.String())
		}
		n, err := p.Read(b)
		buf.Write(b[:n])
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, buf.String())
		}
	}
	return buf.String()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPTYRoundTripOverUnixSocket(t *testing.T) {
	c, l, _ := startGuest(t)
	ctx := context.Background()
	if _, err := c.Ping(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	p, err := c.StartPTY(ctx, "default", PTYSpec{ID: "shell", Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "shell" {
		t.Fatalf("id %s", p.ID())
	}
	waitFor(t, func() bool { return l.count(LeasePTY) == 1 })
	if _, err := p.Write([]byte("echo addr=$LYDIABOX_HOST_ADDR\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "addr="+rpc.HostAddrDocker)

	// A second attachment replays earlier output and shares the room.
	p2, err := c.AttachPTY(ctx, "default", "shell")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p2.Replay()), "addr=unix://") {
		t.Fatalf("replay %q", p2.Replay())
	}
	if err := p2.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := p2.Write([]byte("stty size\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "30 100")
	list, err := c.ListPTYs(ctx, "default")
	if err != nil || len(list) != 1 || !list[0].Running || list[0].Attached != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	if err := p2.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Write([]byte("exit 3\n")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1024)
	for {
		if _, err := p.Read(b); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	code, err := p.Wait(ctx)
	if err != nil || code != 3 {
		t.Fatalf("wait %d %v", code, err)
	}
	waitFor(t, func() bool { return l.count(LeasePTY) == 0 })
	if err := c.KillPTY(ctx, "default", "shell"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AttachPTY(ctx, "default", "shell"); !rpc.IsCode(err, rpc.CodeNotFound) {
		t.Fatalf("attach after kill: %v", err)
	}
}

func TestToolCallModeAndEventsOverUnixSocket(t *testing.T) {
	c, _, fake := startGuest(t)
	ctx := context.Background()
	events, err := c.SubscribeEvents(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	res, err := c.CallTool(ctx, "default", "computer_mouse_click", json.RawMessage(`{"x":10,"y":20}`))
	if err != nil {
		t.Fatal(err)
	}
	var out mcp.ToolResult
	if err := json.Unmarshal(res, &out); err != nil || out.IsError {
		t.Fatalf("result %s %v", res, err)
	}
	if ev := strings.Join(fake.Events(), ","); !strings.HasPrefix(ev, "move 10 20,button 1 down") {
		t.Fatalf("events %s", ev)
	}
	raw, err := events.Recv()
	if err != nil || !strings.Contains(string(raw), `"action":"click"`) {
		t.Fatalf("event %s %v", raw, err)
	}

	st, err := c.SetMode(ctx, "default", rpc.ModeTakeover, "user")
	if err != nil || st.Mode != rpc.ModeTakeover {
		t.Fatalf("SetMode %+v %v", st, err)
	}
	raw, err = events.Recv()
	if err != nil || !strings.Contains(string(raw), `"type":"mode"`) {
		t.Fatalf("mode event %s %v", raw, err)
	}
	res, err = c.CallTool(ctx, "default", "computer_mouse_move", json.RawMessage(`{"x":1,"y":1}`))
	if err != nil || !strings.Contains(string(res), "manual user takeover") {
		t.Fatalf("takeover result %s %v", res, err)
	}
	if got, _ := c.GetMode(ctx, "default"); got.Mode != rpc.ModeTakeover {
		t.Fatalf("mode %+v", got)
	}
	if _, err := c.CallTool(ctx, "default", "nope", nil); !rpc.IsCode(err, rpc.CodeNotFound) {
		t.Fatalf("unknown tool: %v", err)
	}
	tools, err := c.ListTools(ctx, "default")
	if err != nil || !strings.Contains(string(tools), "blender_run_script") {
		t.Fatalf("tools %s %v", tools, err)
	}
	if _, err := c.OpenSignal(ctx, "default", rpc.SignalParams{}); !rpc.IsCode(err, rpc.CodeUnavailable) {
		t.Fatalf("signal without streamer: %v", err)
	}

	// The client reconnects after the connection drops.
	c.Reset("default")
	if _, err := c.Ping(ctx, "default"); err != nil {
		t.Fatal(err)
	}
}
