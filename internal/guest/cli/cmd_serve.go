//go:build unix

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/browser"
	"github.com/lydiabox/lydiabox/internal/guest/credproxy"
	"github.com/lydiabox/lydiabox/internal/guest/mcp"
	"github.com/lydiabox/lydiabox/internal/guest/ptymanager"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/rpc/daemon"
	"github.com/lydiabox/lydiabox/internal/guest/webrtc"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

func init() {
	Register(Command{
		Name:    "serve",
		Summary: "Run the guest daemon (PTY, desktop, MCP, and credential bridges)",
		Run:     runServe,
	})
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

type serveFlags struct {
	listen, hostAddr, gatewayAddr, display, screen string
	mcpAddr, llmAddr, connectorAddr, rtpAddr       string
	ffmpeg, shell, workdir, chromeFlags, logLevel  string
	webrtcPort                                     int
	noDesktop                                      bool
}

func parseServeFlags(env Env, args []string) (*serveFlags, error) {
	f := &serveFlags{}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	port, _ := strconv.Atoi(envOr(rpc.EnvWebRTCPort, strconv.Itoa(rpc.DefaultWebRTCPort)))
	fs.StringVar(&f.listen, "listen", envOr(rpc.EnvGuestListen, rpc.GuestListenVsock), "RPC listen address: vsock://:<port>, unix:///path, or ws://<host>:<port>[/path] (host link)")
	fs.StringVar(&f.hostAddr, "host-addr", envOr(rpc.EnvHostAddr, rpc.HostAddrVsock), "host connector broker address (port 9001)")
	fs.StringVar(&f.gatewayAddr, "gateway-addr", os.Getenv(rpc.EnvGatewayAddr), "host LLM gateway tunnel address (default: port 9002 next to -host-addr)")
	fs.StringVar(&f.display, "display", envOr("DISPLAY", ":0"), "X display")
	fs.StringVar(&f.screen, "screen", "1920x1080", "desktop size for streaming")
	fs.StringVar(&f.mcpAddr, "mcp-addr", mcp.DefaultAddr, "Computer Use MCP listen address (empty disables)")
	fs.StringVar(&f.llmAddr, "llm-addr", credproxy.GatewayListenAddr, "in-guest LLM gateway listen address (empty disables)")
	fs.StringVar(&f.connectorAddr, "connector-mcp-addr", credproxy.ConnectorMCPListenAddr, "in-guest connector MCP listen address (empty disables)")
	fs.StringVar(&f.rtpAddr, "rtp-addr", "127.0.0.1:5004", "ffmpeg RTP output address")
	fs.IntVar(&f.webrtcPort, "webrtc-port", port, "UDP port for all WebRTC media (0 uses ephemeral ports)")
	fs.StringVar(&f.ffmpeg, "ffmpeg", "ffmpeg", "ffmpeg binary")
	fs.StringVar(&f.shell, "shell", "", "shell for PTYs without a command (default $SHELL or /bin/bash)")
	fs.StringVar(&f.workdir, "workdir", "/workspace", "default PTY working directory")
	fs.StringVar(&f.chromeFlags, "chrome-flags", os.Getenv("LYDIABOX_CHROME_FLAGS"), "extra Chrome flags, space separated")
	fs.StringVar(&f.logLevel, "log-level", envOr("LYDIABOX_LOG_LEVEL", "info"), "debug, info, warn, or error")
	fs.BoolVar(&f.noDesktop, "no-desktop", false, "disable X11, WebRTC, and the Computer Use MCP server")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })
	linkDefaults(f, set)
	if f.gatewayAddr == "" {
		addr, err := rpc.SubAddr(f.hostAddr, rpc.PortHostGateway)
		if err != nil {
			return nil, err
		}
		f.gatewayAddr = addr
	}
	return f, nil
}

func runServe(ctx context.Context, env Env, args []string) error {
	f, err := parseServeFlags(env, args)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(f.logLevel)); err != nil {
		return fmt.Errorf("invalid -log-level %q", f.logLevel)
	}
	log := slog.New(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level}))

	workdir := f.workdir
	if fi, err := os.Stat(workdir); err != nil || !fi.IsDir() {
		workdir, _ = os.UserHomeDir()
	}
	ptyEnv := ptymanager.DefaultEnv(f.hostAddr)
	ptyEnv["DISPLAY"] = f.display
	ptys := ptymanager.New(ptymanager.Options{Shell: f.shell, DefaultDir: workdir, Env: ptyEnv, Logger: log})
	opts := daemon.Options{PTY: ptys}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	// On an early error return, stop the goroutines that already started.
	defer func() {
		cancel()
		wg.Wait()
	}()
	errs := make(chan error, 8)
	run := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil && ctx.Err() == nil {
				select {
				case errs <- fmt.Errorf("%s: %w", name, err):
				default:
				}
			}
		}()
	}
	ln, err := listenRPC(ctx, f, log, run)
	if err != nil {
		return err
	}

	var stream *webrtc.Streamer
	if !f.noDesktop {
		stream, err = startDesktop(ctx, f, log, &opts, workdir, run)
		if err != nil {
			ln.Close()
			return err
		}
	}
	if f.llmAddr != "" {
		gl, err := net.Listen("tcp", f.llmAddr)
		if err != nil {
			ln.Close()
			return fmt.Errorf("listen LLM gateway %s: %w", f.llmAddr, err)
		}
		run("llm gateway", func() error { return credproxy.ServeTunnel(ctx, gl, credproxy.AddrDialer(f.gatewayAddr), log) })
	}
	if f.connectorAddr != "" {
		run("connector mcp", func() error {
			return serveHTTP(ctx, f.connectorAddr, credproxy.ConnectorMCPProxy(credproxy.AddrDialer(f.hostAddr)))
		})
	}
	srv := rpc.NewServer(log)
	daemon.Register(srv, opts)
	run("rpc", func() error { return srv.Serve(ctx, ln) })
	log.Info("lydiabox-guest serving", "listen", f.listen, "hostAddr", f.hostAddr, "gatewayAddr", f.gatewayAddr, "desktop", !f.noDesktop)

	select {
	case <-ctx.Done():
		err = nil
	case err = <-errs:
	}
	log.Info("lydiabox-guest shutting down")
	cancel()
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	ptys.Close(sctx)
	if stream != nil {
		_ = stream.Close()
	}
	wg.Wait()
	return err
}

func startDesktop(ctx context.Context, f *serveFlags, log *slog.Logger, opts *daemon.Options, workdir string,
	run func(string, func() error)) (*webrtc.Streamer, error) {
	var w, h int
	if _, err := fmt.Sscanf(f.screen, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return nil, fmt.Errorf("invalid -screen %q", f.screen)
	}
	display := f.display
	desk := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return x11.DialXGB(display) }})
	apps := mcp.NewLocalApps(mcp.LocalAppsOptions{Display: display, WorkDir: desktopDir(workdir), ChromeFlags: strings.Fields(f.chromeFlags)})
	server := mcp.NewServer(desk, apps, mcp.WithBrowser(browser.New(browser.Options{Endpoint: apps.CDPEndpoint, Desktop: desk})))
	stream, err := webrtc.New(webrtc.Options{
		Display: display, Width: w, Height: h, RTPAddr: f.rtpAddr, UDPPort: f.webrtcPort,
		Encoder: webrtc.ExecLauncher{Path: f.ffmpeg, Stderr: os.Stderr}, Input: desk.HandleInputJSON, Logger: log,
	})
	if err != nil {
		return nil, err
	}
	opts.Desktop, opts.MCP, opts.Apps, opts.Stream = desk, server, apps, stream
	if f.mcpAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/mcp", server)
		run("computer use mcp", func() error { return serveHTTP(ctx, f.mcpAddr, mux) })
	}
	return stream, nil
}

func serveHTTP(ctx context.Context, addr string, h http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serveHTTPListener(ctx, ln, h)
}

func serveHTTPListener(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}
	stop := context.AfterFunc(ctx, func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	})
	defer stop()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// desktopDir is the working directory of the programs that the guest
// starts on the desktop (Terminal, Chrome, Blender): the agent home by the
// path in $HOME (/home/agent, also when it is a link), as for the desktop
// session itself. Without a home directory it is the PTY directory.
func desktopDir(workdir string) string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return workdir
	}
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return workdir
	}
	return filepath.Clean(home)
}
