// Package broker is the host connector broker that the guest reaches over
// vsock port 9001 (or the docker driver's UNIX socket). It serves two
// endpoints on any net.Listener:
//
//	POST /git-cred  {"protocol":"https","host":"github.com","path":""}
//	                -> {"username":"x-access-token","password":"<fresh token>"}
//	POST /mcp       MCP JSON-RPC 2.0 (initialize, tools/list, tools/call) with
//	                the github_*, gdrive_*, notion_*, and slack_* tools
//
// Every peer that can connect to the listener can obtain tokens, so the
// listener must only be reachable from the guest: the vsock port of the
// tenant's VM, or a 0600 UNIX socket. Tokens are issued from memory per
// request; nothing is written to the guest disk.
package broker

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/connector"
	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/storage/files"
)

// Default transfer limits of the Google Drive workspace tools.
const (
	DefaultMaxDownloadBytes int64 = 1 << 30
	DefaultMaxUploadBytes   int64 = 1 << 30
)

// Options configures a Broker.
type Options struct {
	Manager *connector.Manager
	// TenantID is the tenant whose guest connects to this broker.
	TenantID string
	// Files is the guest filesystem used by the Google Drive workspace
	// tools (agentfs.Local in juicefs mode, remotefs in provider mode).
	Files  agentfs.FS
	Logger *slog.Logger
	// MaxDownloadBytes and MaxUploadBytes bound gdrive transfers.
	MaxDownloadBytes int64
	MaxUploadBytes   int64
	// ServerVersion is reported by MCP initialize.
	ServerVersion string
}

// Broker serves git credentials and the connector MCP server to one tenant's
// guest. It is an http.Handler.
type Broker struct {
	mgr      *connector.Manager
	tenantID string
	files    *files.Service
	log      *slog.Logger
	maxDown  int64
	maxUp    int64
	version  string
	tools    []tool
	byName   map[string]*tool
	mux      *http.ServeMux
}

// New builds a Broker.
func New(opts Options) (*Broker, error) {
	if opts.Manager == nil {
		return nil, errors.New("broker: Manager is required")
	}
	if opts.TenantID == "" {
		return nil, errors.New("broker: TenantID is required")
	}
	if opts.Files == nil {
		return nil, errors.New("broker: Files is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxDownloadBytes <= 0 {
		opts.MaxDownloadBytes = DefaultMaxDownloadBytes
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if opts.ServerVersion == "" {
		opts.ServerVersion = "dev"
	}
	b := &Broker{
		mgr: opts.Manager, tenantID: opts.TenantID, files: files.NewService(opts.Files, files.Options{}),
		log:     opts.Logger.With("component", "connector-broker", "tenant", opts.TenantID),
		maxDown: opts.MaxDownloadBytes, maxUp: opts.MaxUploadBytes, version: opts.ServerVersion,
		byName: map[string]*tool{},
	}
	for _, set := range [][]tool{githubTools(), gdriveTools(), notionTools(), slackTools()} {
		b.tools = append(b.tools, set...)
	}
	for i := range b.tools {
		t := &b.tools[i]
		if b.byName[t.name] != nil {
			return nil, errors.New("broker: duplicate tool " + t.name)
		}
		b.byName[t.name] = t
	}
	b.mux = http.NewServeMux()
	b.mux.HandleFunc("POST /git-cred", b.gitCred)
	b.mux.HandleFunc("POST /mcp", b.mcp)
	b.mux.HandleFunc("GET /mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "POST")
		httpx.WriteError(w, http.StatusMethodNotAllowed, httpx.CodeBadRequest, "the connector MCP server has no SSE stream; use POST")
	})
	return b, nil
}

// TenantID returns the tenant this broker serves.
func (b *Broker) TenantID() string { return b.tenantID }

// ServeHTTP implements http.Handler.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	b.mux.ServeHTTP(w, r)
}

// Serve serves the broker on ln until ctx is cancelled, then shuts down
// gracefully. It closes ln. A cancelled context is not an error.
func (b *Broker) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           b,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
		ErrorLog:          slog.NewLogLogger(b.log.Handler(), slog.LevelWarn),
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(sctx); err != nil {
				srv.Close()
			}
		case <-done:
		}
	}()
	err := srv.Serve(ln)
	close(done)
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// GitCredRequest is the body of POST /git-cred: the fields git passes to a
// credential helper.
type GitCredRequest struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	Username string `json:"username,omitempty"`
}

// GitCredResponse is the answer of POST /git-cred.
type GitCredResponse struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// GitUsername is the user name git sends with a GitHub token.
const GitUsername = "x-access-token"

func (b *Broker) gitCred(w http.ResponseWriter, r *http.Request) {
	var req GitCredRequest
	if err := httpx.DecodeJSON(w, r, 64<<10, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeBadRequest, err.Error())
		return
	}
	host := strings.ToLower(strings.TrimSpace(req.Host))
	if req.Protocol != "https" || host == "" {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "credentials are issued only for https remotes")
		return
	}
	provider, label, err := b.mgr.GitAccount(r.Context(), b.tenantID, host, req.Username)
	if err != nil {
		b.writeCredError(w, err)
		return
	}
	cred, err := b.mgr.IssueToken(r.Context(), b.tenantID, provider, label, "git-cred", map[string]string{"host": host})
	if err != nil {
		b.writeCredError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, GitCredResponse{Username: GitUsername, Password: cred.AccessToken})
}

func (b *Broker) writeCredError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, connector.ErrNotConnected), errors.Is(err, connector.ErrExpired):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, err.Error())
	default:
		b.log.Error("git credential request failed", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "credential lookup failed")
	}
}
