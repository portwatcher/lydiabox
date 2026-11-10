package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
)

// WebDAVPrefix is the public URL prefix of the WebDAV gateway.
const WebDAVPrefix = "/webdav"

// WebDAVOptions configures a WebDAV gateway.
type WebDAVOptions struct {
	Config        *config.Config
	JuiceFSBinary string
	Runner        Runner
	Logger        *slog.Logger
	ReadyTimeout  time.Duration
	// Dial checks backend readiness; defaults to a TCP dial.
	Dial func(ctx context.Context, addr string) error
}

// WebDAV supervises `juicefs webdav` on a random localhost port and proxies
// /webdav/ to it. The backend requires an internal random Basic Auth
// credential that only this proxy knows; client authentication (Basic
// lydiabox:<LYDIABOX_AUTH_TOKEN>) is enforced by the API auth middleware.
type WebDAV struct {
	opts   WebDAVOptions
	jfs    JuiceFS
	log    *slog.Logger
	tenant string
	user   string
	pass   string

	mu       sync.RWMutex
	addr     string
	started  bool
	stopping chan struct{}
	done     chan struct{}
	proc     Process
	watch    *procWatch
}

// NewWebDAV validates options.
func NewWebDAV(opts WebDAVOptions) (*WebDAV, error) {
	if opts.Config == nil || opts.Runner == nil || opts.JuiceFSBinary == "" {
		return nil, errors.New("storage: webdav needs Config, Runner, and JuiceFSBinary")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 60 * time.Second
	}
	if opts.Dial == nil {
		opts.Dial = func(ctx context.Context, addr string) error {
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", addr)
			if err == nil {
				c.Close()
			}
			return err
		}
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	c := opts.Config
	return &WebDAV{
		opts:   opts,
		tenant: c.TenantID,
		log:    opts.Logger.With("component", "webdav", "tenant", c.TenantID),
		jfs:    JuiceFS{Binary: opts.JuiceFSBinary, MetaURL: c.JuiceFSMetaURL(), MountDir: c.MountDir()},
		user:   "lydiabox-internal",
		pass:   hex.EncodeToString(secret),
	}, nil
}

// Addr returns the backend address, or "" when the backend is not running.
func (w *WebDAV) Addr() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.addr
}

// Start launches the backend and its supervisor goroutine.
func (w *WebDAV) Start(ctx context.Context, tenantID string) error {
	if tenantID != w.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("storage: webdav already started")
	}
	if err := w.launch(ctx); err != nil {
		return err
	}
	w.started = true
	w.stopping = make(chan struct{})
	w.done = make(chan struct{})
	go w.supervise()
	return nil
}

// launch must be called with w.mu held.
func (w *WebDAV) launch(ctx context.Context) error {
	port, err := freePort()
	if err != nil {
		return fmt.Errorf("storage: webdav port: %w", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	cmd := w.jfs.WebDAV(addr, filepath.Join(w.opts.Config.DataDir, "cache", "jfs-webdav"), w.user, w.pass)
	out := newLogWriter(w.log, "juicefs webdav")
	cmd.Stdout, cmd.Stderr = out, out
	proc, err := w.opts.Runner.Start(cmd)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	watch := watchProcess(proc)
	deadline := time.Now().Add(w.opts.ReadyTimeout)
	for {
		dctx, cancel := context.WithTimeout(ctx, time.Second)
		err := w.opts.Dial(dctx, addr)
		cancel()
		if err == nil {
			break
		}
		select {
		case <-watch.done:
			return fmt.Errorf("storage: juicefs webdav exited before ready: %v", watch.err)
		case <-ctx.Done():
			stopProcess(proc, watch)
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			stopProcess(proc, watch)
			return fmt.Errorf("storage: juicefs webdav not ready on %s after %s", addr, w.opts.ReadyTimeout)
		}
	}
	w.addr, w.proc, w.watch = addr, proc, watch
	w.log.Info("WebDAV backend ready", "addr", addr, "pid", proc.Pid())
	return nil
}

func (w *WebDAV) supervise() {
	defer close(w.done)
	backoff := time.Second
	for {
		w.mu.RLock()
		watch := w.watch
		w.mu.RUnlock()
		select {
		case <-w.stopping:
			return
		case <-watch.done:
		}
		w.mu.Lock()
		w.addr = ""
		w.mu.Unlock()
		w.log.Error("juicefs webdav exited; restarting", "err", watch.err, "backoff", backoff)
		select {
		case <-w.stopping:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-w.stopping:
				cancel()
			case <-ctx.Done():
			}
		}()
		w.mu.Lock()
		err := w.launch(ctx)
		w.mu.Unlock()
		cancel()
		if err != nil {
			w.log.Error("WebDAV restart failed", "err", err)
			// Keep a closed watch so the loop retries after the backoff.
			w.mu.Lock()
			w.watch = &procWatch{done: closedChan(), err: err}
			w.mu.Unlock()
			continue
		}
		backoff = time.Second
	}
}

// Stop terminates the backend.
func (w *WebDAV) Stop(ctx context.Context, tenantID string) error {
	if tenantID != w.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return nil
	}
	w.started = false
	close(w.stopping)
	w.mu.Unlock()
	select {
	case <-w.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	w.mu.Lock()
	proc, watch := w.proc, w.watch
	w.addr = ""
	w.mu.Unlock()
	if proc != nil {
		stopProcess(proc, watch)
	}
	return nil
}

func stopProcess(proc Process, w *procWatch) {
	_ = proc.Signal(syscall.SIGTERM)
	select {
	case <-w.done:
	case <-time.After(15 * time.Second):
		_ = proc.Kill()
		<-w.done
	}
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Handler returns the reverse proxy for /webdav/. It answers 503 while the
// backend is down. Authentication must be applied by the caller.
func (w *WebDAV) Handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		addr := w.Addr()
		if addr == "" {
			http.Error(rw, "WebDAV backend is not running", http.StatusServiceUnavailable)
			return
		}
		NewWebDAVProxy(addr, w.user, w.pass).ServeHTTP(rw, r)
	})
}

// NewWebDAVProxy proxies requests under WebDAVPrefix to a WebDAV server at
// backendAddr that serves the volume at "/". It rewrites request paths, the
// Destination header, Location headers, and multistatus <href> elements.
func NewWebDAVProxy(backendAddr, user, pass string) http.Handler {
	backend := &url.URL{Scheme: "http", Host: backendAddr}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(backend)
			pr.Out.URL.Path = stripWebDAVPrefix(pr.In.URL.Path)
			pr.Out.URL.RawPath = ""
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Accept-Encoding")
			pr.Out.SetBasicAuth(user, pass)
			if dst := pr.In.Header.Get("Destination"); dst != "" {
				if u, err := url.Parse(dst); err == nil {
					pr.Out.Header.Set("Destination", backend.String()+stripWebDAVPrefix(u.Path))
				}
			}
		},
		ModifyResponse: rewriteWebDAVResponse,
	}
}

func stripWebDAVPrefix(p string) string {
	rest := strings.TrimPrefix(p, WebDAVPrefix)
	if rest == "" || rest[0] != '/' {
		rest = "/" + rest
	}
	return rest
}

var hrefPattern = regexp.MustCompile(`(<(?:[A-Za-z][A-Za-z0-9_.-]*:)?href>)\s*(?:https?://[^/<]+)?/`)

func rewriteWebDAVResponse(resp *http.Response) error {
	if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, "/") {
		resp.Header.Set("Location", WebDAVPrefix+loc)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "xml") || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	const maxXML = 64 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxXML+1))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if len(body) > maxXML {
		return fmt.Errorf("storage: WebDAV XML response exceeds %d bytes", maxXML)
	}
	body = hrefPattern.ReplaceAll(body, []byte("${1}"+WebDAVPrefix+"/"))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}
