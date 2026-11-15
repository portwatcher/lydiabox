//go:build unix

package link

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/credproxy"
	guestlink "github.com/lydiabox/lydiabox/internal/guest/link"
	"github.com/lydiabox/lydiabox/internal/guest/mcp"
	"github.com/lydiabox/lydiabox/internal/guest/ptymanager"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/rpc/daemon"
	"github.com/lydiabox/lydiabox/internal/guest/webrtc"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
)

var (
	testSecret = []byte("0123456789abcdef-link-secret")
	testToken  = "ibp_test_token"
)

// checkLeaks fails the test when goroutines started during the test are
// still running after every cleanup. Call it first, so that its cleanup
// runs last.
func checkLeaks(t *testing.T) {
	t.Helper()
	base := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for runtime.NumGoroutine() > base {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d running, %d before the test\n%s", runtime.NumGoroutine(), base, buf[:n])
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
}

// tracker records the connections that a listener accepts, so that a test
// can drop them (hijacked WebSocket connections included).
type tracker struct {
	net.Listener
	mu    sync.Mutex
	conns map[net.Conn]bool
}

func (t *tracker) Accept() (net.Conn, error) {
	c, err := t.Listener.Accept()
	if err == nil {
		t.mu.Lock()
		t.conns[c] = true
		t.mu.Unlock()
	}
	return c, err
}

func (t *tracker) drop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for c := range t.conns {
		c.Close()
	}
	t.conns = map[net.Conn]bool{}
}

type rigOptions struct {
	hostSecret []byte
	token      string
	desktop    bool
	// upgradeDelay delays each WebSocket upgrade in the proxy (a slow wake).
	upgradeDelay time.Duration
}

// rig is the real guest daemon behind a link endpoint, an HTTPS-like
// reverse proxy that requires a token, and the host link.
type rig struct {
	guest  *guestlink.Guest
	proxy  *tracker
	url    string
	link   *Link
	client *guestclient.Client
	http   *http.Client
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

func startGuestDaemon(t *testing.T, desktop bool) *guestlink.Guest {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	g, err := guestlink.NewGuest(guestlink.GuestOptions{Secret: testSecret, Logger: log, DialWait: 5 * time.Second, KeepAlive: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := g.Listen(rpc.PortGuestRPC)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ptys := ptymanager.New(ptymanager.Options{Logger: log, Shell: "/bin/sh", DefaultDir: dir, Env: ptymanager.DefaultEnv(rpc.HostAddrDocker)})
	opts := daemon.Options{PTY: ptys}
	var stream *webrtc.Streamer
	if desktop {
		fake := x11.NewFake(1280, 720)
		desk := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return fake, nil }})
		stream, err = webrtc.New(webrtc.Options{RTPAddr: "127.0.0.1:0", Encoder: quietEncoder{}, IncludeLoopback: true, Logger: log, Width: 1280, Height: 720})
		if err != nil {
			t.Fatal(err)
		}
		opts.Desktop, opts.MCP, opts.Apps, opts.Stream = desk, mcp.NewServer(desk, noApps{}), noApps{}, stream
	}
	srv := rpc.NewServer(log)
	daemon.Register(srv, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		g.Close()
		cancel()
		<-done
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		ptys.Close(cctx)
		if stream != nil {
			_ = stream.Close()
		}
	})
	return g
}

func newRig(t *testing.T, o rigOptions) *rig {
	t.Helper()
	if o.hostSecret == nil {
		o.hostSecret = testSecret
	}
	if o.token == "" {
		o.token = testToken
	}
	g := startGuestDaemon(t, o.desktop)
	backend := httptest.NewServer(g)
	t.Cleanup(backend.Close)
	target, _ := url.Parse(backend.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "missing or invalid port token", http.StatusUnauthorized)
			return
		}
		if o.upgradeDelay > 0 {
			time.Sleep(o.upgradeDelay)
		}
		rp.ServeHTTP(w, r)
	}))
	tr := &tracker{Listener: front.Listener, conns: map[net.Conn]bool{}}
	front.Listener = tr
	front.Start()
	t.Cleanup(func() {
		tr.drop()
		front.Close()
	})

	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	r := &rig{guest: g, proxy: tr, url: "ws" + strings.TrimPrefix(front.URL, "http") + "/link", http: &http.Client{Transport: transport}}
	l, err := New(Options{Secret: o.hostSecret, MinBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
		KeepAlive: time.Second, WakeTimeout: 10 * time.Second, HTTPClient: r.http, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	r.link = l
	r.client = guestclient.New(guestclient.Options{Dialer: singleDialer{l}, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { r.client.Reset("default") })
	if err := l.Connect(Target{URL: r.url, Header: http.Header{"Authorization": {"Bearer " + o.token}}}); err != nil {
		t.Fatal(err)
	}
	return r
}

type singleDialer struct{ l *Link }

func (d singleDialer) DialGuest(ctx context.Context, _ string, port uint32) (net.Conn, error) {
	return d.l.Dial(ctx, port)
}

// waitState waits until the link reaches st.
func waitState(t *testing.T, l *Link, st State) Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		s := l.Status()
		if s.State == st {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("link state %s (%s), want %s", s.State, s.LastError, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// serveHost serves an HTTP handler on a host port of the link, as the
// connector broker (9001) and the LLM gateway (9002) do.
func serveHost(t *testing.T, l *Link, port uint32, body string) {
	t.Helper()
	ln, err := l.Listen(port)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		srv.Close()
		<-done
	})
}

// guestGet makes a request from the guest to a host port: through a local
// UNIX bridge, as the daemon does in link mode.
func guestGet(t *testing.T, g *guestlink.Guest, port uint32) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lbxl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := rpc.ListenUnix(dir+"/host.sock", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = credproxy.ServeTunnel(ctx, ln, func(ctx context.Context) (net.Conn, error) { return g.Dial(ctx, port) }, slog.New(slog.DiscardHandler))
	}()
	defer func() {
		cancel()
		<-done
	}()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", dir+"/host.sock")
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get("http://host/")
	if err != nil {
		t.Fatalf("guest request to host port %d: %v", port, err)
	}
	defer resp.Body.Close()
	b := make([]byte, 256)
	n, _ := resp.Body.Read(b)
	return string(b[:n])
}
