// Package api is the host HTTP/WebSocket server: one net/http ServeMux, the
// auth middleware, and the route registration contract that every subsystem
// uses to add its endpoints.
//
// # Adding routes (later phases)
//
// Create internal/api/routes_<subsystem>.go with a type that implements
// RouteRegistrar, then add one line to the registrar list in
// internal/cli/up.go. Do not edit server.go or another subsystem's file.
//
//	type VMRoutes struct{ Driver vmm.Driver }
//
//	func (v VMRoutes) RegisterRoutes(r Router) {
//		r.HandleFunc("POST /api/v1/vm/wake", v.wake)          // auth required
//		r.HandleFunc("GET /api/v1/connectors/{provider}/callback", cb, Public())
//	}
//
// Patterns use the Go 1.22 ServeMux syntax ("METHOD /path/{wildcard}").
// Handlers read the tenant with tenant.FromContext(r.Context()) and write
// JSON with the internal/httpx helpers. docs/api.md is the contract.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// Router registers handlers on the server mux. Routes require authentication
// unless registered with Public().
type Router interface {
	Handle(pattern string, h http.Handler, opts ...RouteOption)
	HandleFunc(pattern string, h http.HandlerFunc, opts ...RouteOption)
}

// RouteRegistrar is implemented by each subsystem's routes_<subsystem>.go.
type RouteRegistrar interface {
	RegisterRoutes(r Router)
}

// RegistrarFunc adapts a function to RouteRegistrar.
type RegistrarFunc func(r Router)

// RegisterRoutes implements RouteRegistrar.
func (f RegistrarFunc) RegisterRoutes(r Router) { f(r) }

// RouteOption changes how a route is protected.
type RouteOption func(*routeConfig)

type routeConfig struct {
	public         bool
	basicChallenge bool
}

// Public skips authentication (health checks, OAuth callbacks, static UI).
func Public() RouteOption { return func(c *routeConfig) { c.public = true } }

// BasicChallenge makes unauthenticated responses carry
// `WWW-Authenticate: Basic`, which WebDAV clients need to prompt for
// credentials.
func BasicChallenge() RouteOption { return func(c *routeConfig) { c.basicChallenge = true } }

// Options configures a Server.
type Options struct {
	Addr     string
	Auth     *Authenticator
	TenantID string
	Logger   *slog.Logger
}

// Server is the HTTP server.
type Server struct {
	opts Options
	mux  *http.ServeMux
	log  *slog.Logger

	mu      sync.Mutex
	srv     *http.Server
	ln      net.Listener
	pattern map[string]bool
}

// NewServer creates a server and registers the given registrars in order.
// Registering the same pattern twice panics, which surfaces route conflicts
// between subsystems at startup.
func NewServer(opts Options, registrars ...RouteRegistrar) (*Server, error) {
	if opts.Auth == nil {
		return nil, errors.New("api: Auth is required")
	}
	if opts.TenantID == "" {
		return nil, errors.New("api: TenantID is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{opts: opts, mux: http.NewServeMux(), log: opts.Logger.With("component", "api"), pattern: map[string]bool{}}
	for _, reg := range registrars {
		reg.RegisterRoutes(s)
	}
	return s, nil
}

// Handle implements Router.
func (s *Server) Handle(pattern string, h http.Handler, opts ...RouteOption) {
	var cfg routeConfig
	for _, o := range opts {
		o(&cfg)
	}
	if s.pattern[pattern] {
		panic(fmt.Sprintf("api: route %q registered twice", pattern))
	}
	s.pattern[pattern] = true
	if !cfg.public {
		h = s.opts.Auth.Require(h, cfg.basicChallenge)
	}
	s.mux.Handle(pattern, withRoute(pattern, h))
}

// HandleFunc implements Router.
func (s *Server) HandleFunc(pattern string, h http.HandlerFunc, opts ...RouteOption) {
	s.Handle(pattern, h, opts...)
}

// Handler returns the root handler with recovery, logging, security headers,
// and tenant context applied.
func (s *Server) Handler() http.Handler {
	return s.recoverer(s.logRequests(securityHeaders(s.withTenant(s.mux))))
}

func (s *Server) withTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(tenant.WithID(r.Context(), s.opts.TenantID)))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/webdav") {
			h.Set("X-Frame-Options", "DENY")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("handler panic", "panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Listen binds the listen address. It is separate from Serve so the CLI can
// fail fast on an address conflict before starting background services.
//
// An address "unix:/path" (or "unix:///path") listens on a UNIX socket with
// mode 0660, for a cell host that forwards requests to the cell.
func (s *Server) Listen() (net.Addr, error) {
	ln, err := listen(s.opts.Addr)
	if err != nil {
		return nil, fmt.Errorf("api: listen %s: %w", s.opts.Addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	s.mu.Unlock()
	return ln.Addr(), nil
}

func listen(addr string) (net.Listener, error) {
	rest, ok := strings.CutPrefix(addr, "unix:")
	if !ok {
		return net.Listen("tcp", addr)
	}
	path := strings.TrimPrefix(rest, "//")
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		// A socket left by a cell that was killed.
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve serves on the listener from Listen until Shutdown.
func (s *Server) Serve() error {
	s.mu.Lock()
	srv, ln := s.srv, s.ln
	s.mu.Unlock()
	if srv == nil {
		return errors.New("api: Listen must be called before Serve")
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting connections and waits for active requests.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}
