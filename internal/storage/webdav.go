package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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
	tenant string
	user   string
	pass   string
	child  *childServer
}

// NewWebDAV validates options.
func NewWebDAV(opts WebDAVOptions) (*WebDAV, error) {
	if opts.Config == nil || opts.Runner == nil || opts.JuiceFSBinary == "" {
		return nil, errNoBinary
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 60 * time.Second
	}
	if opts.Dial == nil {
		opts.Dial = defaultDial
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	c := opts.Config
	w := &WebDAV{tenant: c.TenantID, user: "lydiabox-internal", pass: hex.EncodeToString(secret)}
	jfs := JuiceFS{Binary: opts.JuiceFSBinary, MetaURL: c.JuiceFSMetaURL(), MountDir: c.MountDir()}
	cacheDir := filepath.Join(c.DataDir, "cache", "jfs-webdav")
	w.child = &childServer{
		name: "juicefs webdav", log: opts.Logger.With("component", "webdav", "tenant", c.TenantID),
		runner: opts.Runner, dial: opts.Dial, readyTimeout: opts.ReadyTimeout,
		command: func(addr string) Command { return jfs.WebDAV(addr, cacheDir, w.user, w.pass) },
	}
	return w, nil
}

// Addr returns the backend address, or "" when the backend is not running.
func (w *WebDAV) Addr() string { return w.child.Addr() }

// Start launches the backend and its supervisor goroutine.
func (w *WebDAV) Start(ctx context.Context, tenantID string) error {
	if tenantID != w.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	return w.child.start(ctx)
}

// Stop terminates the backend.
func (w *WebDAV) Stop(ctx context.Context, tenantID string) error {
	if tenantID != w.tenant {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	return w.child.stop(ctx)
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
