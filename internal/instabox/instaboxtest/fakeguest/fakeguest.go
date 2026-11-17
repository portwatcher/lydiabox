//go:build unix

// Package fakeguest runs the real LydiaBox guest daemon in the test
// process for the machines of the fake InstaBox server
// (instaboxtest.GuestRunner). Like the image's wrapper
// (deploy/instabox/lydiabox-instabox-guest), it waits for
// /data/.lydiabox/guest.env, reads the link listener and the secret from
// it, and serves the host link: the guest RPC (PTYs, files, desktop
// signaling), and the UNIX bridges to host ports 9001 and 9002. The /data
// disk of each machine is a local directory. The desktop has a fake X
// display and an encoder that sends nothing, so signaling works without
// X11 or ffmpeg.
package fakeguest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/credproxy"
	"github.com/lydiabox/lydiabox/internal/guest/guestfs"
	guestlink "github.com/lydiabox/lydiabox/internal/guest/link"
	"github.com/lydiabox/lydiabox/internal/guest/mcp"
	"github.com/lydiabox/lydiabox/internal/guest/ptymanager"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/rpc/daemon"
	"github.com/lydiabox/lydiabox/internal/guest/webrtc"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

// EnvPath is the settings file that the driver writes.
const EnvPath = "/data/.lydiabox/guest.env"

// Runner implements instaboxtest.GuestRunner.
type Runner struct {
	root string
	log  *slog.Logger

	mu       sync.Mutex
	machines map[string]*machine
	boots    map[string]int
}

type machine struct {
	cancel context.CancelFunc
	done   chan struct{}
	files  func(string) ([]byte, bool)

	mu   sync.Mutex
	g    *guest
	port int
}

// New returns a runner. root holds the /data directory of each machine;
// keep it short, because it holds UNIX sockets.
func New(root string, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runner{root: root, log: log, machines: map[string]*machine{}, boots: map[string]int{}}
}

// PTYs lists the terminals of the guest daemon of machine id (nil when it
// does not run).
func (r *Runner) PTYs(id string) []rpc.PTYInfo {
	r.mu.Lock()
	m := r.machines[id]
	r.mu.Unlock()
	if m == nil {
		return nil
	}
	m.mu.Lock()
	g := m.g
	m.mu.Unlock()
	if g == nil || g.ptys == nil {
		return nil
	}
	return g.ptys.List()
}

// DataDir returns the local directory that is /data of machine id.
func (r *Runner) DataDir(id string) string {
	return filepath.Join(r.root, strings.TrimPrefix(id, "m_")[:8])
}

// RunDir returns the directory of the bridge sockets of machine id
// (/run/lydiabox in the image).
func (r *Runner) RunDir(id string) string { return filepath.Join(r.DataDir(id), "run") }

// Boots returns how often the programs of machine id started.
func (r *Runner) Boots(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.boots[id]
}

// Boot implements instaboxtest.GuestRunner: it waits for guest.env in the
// background and then starts the daemon.
func (r *Runner) Boot(id string, files func(string) ([]byte, bool)) {
	r.Halt(id)
	ctx, cancel := context.WithCancel(context.Background())
	m := &machine{cancel: cancel, done: make(chan struct{}), files: files}
	r.mu.Lock()
	r.machines[id] = m
	r.boots[id]++
	r.mu.Unlock()
	go func() {
		defer close(m.done)
		r.run(ctx, id, m)
	}()
}

// Restart stops and starts the daemon of machine id, as
// `supervisorctl restart lydiabox-guest` does in the image.
func (r *Runner) Restart(id string) {
	r.mu.Lock()
	m := r.machines[id]
	r.mu.Unlock()
	if m != nil {
		r.Boot(id, m.files)
	}
}

// Halt implements instaboxtest.GuestRunner.
func (r *Runner) Halt(id string) {
	r.mu.Lock()
	m := r.machines[id]
	delete(r.machines, id)
	r.mu.Unlock()
	if m == nil {
		return
	}
	m.cancel()
	<-m.done
}

// Close stops every machine program.
func (r *Runner) Close() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.machines))
	for id := range r.machines {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.Halt(id)
	}
}

// Addr implements instaboxtest.GuestRunner.
func (r *Runner) Addr(id string, port int) string {
	r.mu.Lock()
	m := r.machines[id]
	r.mu.Unlock()
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.g == nil || m.port != port {
		return ""
	}
	return m.g.addr
}

func (r *Runner) run(ctx context.Context, id string, m *machine) {
	var env map[string]string
	for {
		if b, ok := m.files(EnvPath); ok {
			env = parseEnv(b)
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	g, port, err := r.start(id, env)
	if err != nil {
		r.log.Error("fakeguest: start the guest daemon", "machine", id, "err", err)
		return
	}
	m.mu.Lock()
	m.g, m.port = g, port
	m.mu.Unlock()
	<-ctx.Done()
	m.mu.Lock()
	m.g = nil
	m.mu.Unlock()
	g.close()
}

// parseEnv reads KEY=VALUE lines, as the image's wrapper does.
func parseEnv(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(k, "LYDIABOX_") {
			out[k] = v
		}
	}
	return out
}

// guest is one running daemon.
type guest struct {
	addr   string
	link   *guestlink.Guest
	srv    *http.Server
	ptys   *ptymanager.Manager
	stream *webrtc.Streamer
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func (r *Runner) start(id string, env map[string]string) (*guest, int, error) {
	listen, err := url.Parse(env[rpc.EnvGuestListen])
	if err != nil || listen.Scheme != "ws" {
		return nil, 0, fmt.Errorf("guest.env: %s=%q is not a ws:// listener", rpc.EnvGuestListen, env[rpc.EnvGuestListen])
	}
	port, err := strconv.Atoi(listen.Port())
	if err != nil {
		return nil, 0, fmt.Errorf("guest.env: %s has no port", rpc.EnvGuestListen)
	}
	data := r.DataDir(id)
	// The image's layout (deploy/instabox): the agent home is
	// /data/lydiabox/home unless guest.env names another root.
	home := mapData(data, env[rpc.EnvGuestHomeRoot], "lydiabox/home")
	ws := mapData(data, env[rpc.EnvGuestWorkspaceRoot], "workspace")
	run := r.RunDir(id)
	for _, d := range []string{home, ws, run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, 0, err
		}
	}
	lg, err := guestlink.NewGuest(guestlink.GuestOptions{Secret: []byte(env[guestlink.EnvSecret]), Logger: r.log, DialWait: 5 * time.Second})
	if err != nil {
		return nil, 0, err
	}
	g := &guest{link: lg}
	ok := false
	defer func() {
		if !ok {
			g.close()
		}
	}()
	rpcLn, err := lg.Listen(rpc.PortGuestRPC)
	if err != nil {
		return nil, 0, err
	}
	broker := "unix://" + filepath.Join(run, "host-9001.sock")
	ptyEnv := ptymanager.DefaultEnv(broker)
	ptyEnv["HOME"] = home
	g.ptys = ptymanager.New(ptymanager.Options{Shell: "/bin/sh", DefaultDir: ws, Env: ptyEnv, Logger: r.log,
		Dirs: map[string]string{"/workspace": ws, "/home/agent": home}})
	files, err := guestfs.New(guestfs.Options{Home: home, Workspace: ws})
	if err != nil {
		return nil, 0, err
	}
	fake := x11.NewFake(1280, 720)
	desk := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return fake, nil }})
	g.stream, err = webrtc.New(webrtc.Options{RTPAddr: "127.0.0.1:0", Encoder: quietEncoder{}, IncludeLoopback: true,
		Logger: r.log, Width: 1280, Height: 720})
	if err != nil {
		return nil, 0, err
	}
	srv := rpc.NewServer(r.log)
	daemon.Register(srv, daemon.Options{PTY: g.ptys, Desktop: desk, MCP: mcp.NewServer(desk, noApps{}), Apps: noApps{},
		Stream: g.stream, Files: files})
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	g.goRun(func() { _ = srv.Serve(ctx, rpcLn) })
	for _, b := range []uint32{rpc.PortHostBroker, rpc.PortHostGateway} {
		ln, err := rpc.ListenUnix(filepath.Join(run, fmt.Sprintf("host-%d.sock", b)), 0o666)
		if err != nil {
			return nil, 0, err
		}
		hostPort := b
		g.goRun(func() {
			_ = credproxy.ServeTunnel(ctx, ln, func(ctx context.Context) (net.Conn, error) { return lg.Dial(ctx, hostPort) }, r.log)
		})
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	mux := http.NewServeMux()
	mux.Handle(listen.Path, lg)
	g.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	g.addr = tcp.Addr().String()
	g.goRun(func() {
		if err := g.srv.Serve(tcp); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.log.Error("fakeguest: link listener", "err", err)
		}
	})
	ok = true
	return g, port, nil
}

// mapData maps a guest path under /data to the machine's data directory.
func mapData(data, guestPath, def string) string {
	rel, ok := strings.CutPrefix(guestPath, "/data/")
	if !ok || rel == "" {
		rel = def
	}
	return filepath.Join(data, filepath.FromSlash(rel))
}

func (g *guest) goRun(fn func()) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		fn()
	}()
}

func (g *guest) close() {
	if g.srv != nil {
		_ = g.srv.Close()
	}
	_ = g.link.Close()
	if g.cancel != nil {
		g.cancel()
	}
	g.wg.Wait()
	if g.ptys != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		g.ptys.Close(ctx)
		cancel()
	}
	if g.stream != nil {
		_ = g.stream.Close()
	}
}

type quietEncoder struct{}

type quietProc struct {
	once sync.Once
	done chan struct{}
}

func (quietEncoder) Start([]string) (webrtc.Process, error) {
	return &quietProc{done: make(chan struct{})}, nil
}

func (p *quietProc) Done() <-chan struct{} { return p.done }

func (p *quietProc) Stop() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type noApps struct{}

func (noApps) Launch(context.Context, string) error        { return nil }
func (noApps) CDPEndpoint(context.Context) (string, error) { return "", nil }
func (noApps) RunBlender(context.Context, mcp.BlenderRequest) (mcp.BlenderResult, error) {
	return mcp.BlenderResult{}, nil
}
