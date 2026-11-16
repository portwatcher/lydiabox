package api_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
	"github.com/lydiabox/lydiabox/internal/httpx"
)

// syncBuffer is a log sink that handlers can write from any goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newLoggedServer(t *testing.T, regs ...api.RouteRegistrar) (http.Handler, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: api.NewAuthenticator(token), TenantID: "default", Logger: log}, regs...)
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), buf
}

func authed(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestServerErrorLogsRouteAndCauseOnce(t *testing.T) {
	h, buf := newLoggedServer(t, api.RegistrarFunc(func(r api.Router) {
		r.HandleFunc("GET /api/v1/fail/{id}", func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal,
				`dial https://u:pw@guest.example/x?token=abc123&a=1: Authorization: Bearer sekret-value`)
		})
		r.HandleFunc("GET /api/v1/hidden", func(w http.ResponseWriter, _ *http.Request) {
			httpx.NoteCause(w, errors.New("store: database is locked; api_key=K1"))
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "internal server error")
		})
		r.HandleFunc("GET /api/v1/ok", func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteJSON(w, http.StatusOK, map[string]string{"ok": "yes"})
		})
	}))
	for _, p := range []string{"/api/v1/fail/7", "/api/v1/hidden", "/api/v1/ok"} {
		h.ServeHTTP(httptest.NewRecorder(), authed(http.MethodGet, p))
	}
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 log lines (one per 5xx, none for 200), got %d:\n%s", len(lines), out)
	}
	for _, want := range []string{"level=ERROR", `msg="request failed"`, `route="GET /api/v1/fail/{id}"`, "status=500",
		"internal: dial https://[redacted]@guest.example/x?token=[redacted]&a=1"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("first line lacks %q:\n%s", want, lines[0])
		}
	}
	if !strings.Contains(lines[1], `route="GET /api/v1/hidden"`) || !strings.Contains(lines[1], "database is locked; api_key=[redacted]") {
		t.Errorf("second line lacks the noted cause:\n%s", lines[1])
	}
	for _, secret := range []string{"abc123", "sekret-value", "pw@", "K1"} {
		if strings.Contains(out, secret) {
			t.Errorf("log has the secret %q:\n%s", secret, out)
		}
	}
}

// blockingGuest stands for a guest daemon that does not answer while the
// machine wakes or the link reconnects: the call ends with its context.
type blockingGuest struct{ called chan struct{} }

func (g *blockingGuest) FSCall(ctx context.Context, _, _ string, _, _ any) error {
	close(g.called)
	<-ctx.Done()
	return ctx.Err()
}

// TestSessionListClientGoneIsNotServerError repeats the 500 of
// GET /api/v1/sessions: in provider storage mode the list waits for the
// guest discovery; when the browser closes the request (a reload of the
// page) the discovery returns context.Canceled, which was a 500 with no
// cause in the log.
func TestSessionListClientGoneIsNotServerError(t *testing.T) {
	home, ws := t.TempDir(), t.TempDir()
	guest := &blockingGuest{called: make(chan struct{})}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(),
		Files:     agentfs.NewLocal(harnesstest.Dirs{Home: home, Workspace: ws}, agentfs.LocalOptions{}),
		Discovery: &harness.GuestDiscovery{Guest: guest, Running: func(string) bool { return true }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Store:     &harnesstest.Store{}, Runner: &harnesstest.Runner{}, Waker: &harnesstest.Waker{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), KillGrace: 100 * time.Millisecond, TermGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	h, buf := newLoggedServer(t, api.SessionsRoutes{Manager: mgr})
	ctx, cancel := context.WithCancel(context.Background())
	req := authed(http.MethodGet, "/api/v1/sessions").WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	<-guest.called
	cancel()
	<-done
	if rec.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, httpx.StatusClientClosedRequest, rec.Body)
	}
	if strings.Contains(buf.String(), "request failed") {
		t.Fatalf("a closed request was logged as a server error:\n%s", buf)
	}
}

// A 503 for an expected state (a VM that is not running or that the user
// stopped) is not logged at the error level; other 503s are.
func TestExpectedUnavailableIsNotAnError(t *testing.T) {
	h, buf := newLoggedServer(t, api.RegistrarFunc(func(r api.Router) {
		r.HandleFunc("GET /api/v1/expected", func(w http.ResponseWriter, _ *http.Request) {
			httpx.NoteExpected(w)
			httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, "VM is not running")
		})
		r.HandleFunc("GET /api/v1/fault", func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, "guest link: connection reset")
		})
	}))
	h.ServeHTTP(httptest.NewRecorder(), authed(http.MethodGet, "/api/v1/expected"))
	h.ServeHTTP(httptest.NewRecorder(), authed(http.MethodGet, "/api/v1/fault"))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got:\n%s", buf.String())
	}
	if !strings.Contains(lines[0], "level=INFO") || !strings.Contains(lines[0], "status=503") || !strings.Contains(lines[0], "VM is not running") {
		t.Errorf("expected 503: %s", lines[0])
	}
	if !strings.Contains(lines[1], "level=ERROR") || !strings.Contains(lines[1], "connection reset") {
		t.Errorf("fault 503: %s", lines[1])
	}
}
