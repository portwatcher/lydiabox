package cli

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/bootstrap"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/storage"
	"github.com/lydiabox/lydiabox/internal/store"
)

type dirStorage struct{ home, ws string }

func (d dirStorage) Start(context.Context, string) error   { return nil }
func (d dirStorage) Stop(context.Context, string) error    { return nil }
func (d dirStorage) Status(string) storage.Status          { return storage.Status{Mounted: true} }
func (d dirStorage) SyncNow(context.Context, string) error { return nil }
func (d dirStorage) HomeDir(string) (string, error)        { return d.home, nil }
func (d dirStorage) WorkspaceDir(string) (string, error)   { return d.ws, nil }

func TestWireHarness(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	cfg := config.Defaults(base)
	st, err := store.Open(ctx, filepath.Join(base, "state.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	boot, err := bootstrap.New(bootstrap.Options{BinDir: filepath.Join(base, "bin"), Driver: config.DriverDocker})
	if err != nil {
		t.Fatal(err)
	}
	stor := dirStorage{home: filepath.Join(base, "home"), ws: filepath.Join(base, "ws")}
	for _, p := range []string{stor.home, stor.ws} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &deps{Config: cfg, Driver: config.DriverDocker, Logger: slog.New(slog.DiscardHandler), Store: st, Bootstrap: boot,
		Storage: stor, Files: agentfs.NewLocal(stor, agentfs.LocalOptions{}), Auth: api.NewAuthenticator("test-admin-token-0123456789abcdef"),
		VMStatus: api.StaticVMStatus{State: api.VMStopped, Driver: config.DriverDocker}}
	if _, err := wireVM(ctx, d); err != nil {
		t.Fatal(err)
	}
	c, err := wireHarness(ctx, d)
	if err != nil || c == nil || len(c.Routes) != 2 {
		t.Fatalf("component %+v %v", c, err)
	}
	if r, _ := guestRuntime(d); r == (harness.UnavailableRunner{}) {
		t.Fatal("guest client not used as the PTY runner")
	}
	if r, w := guestRuntime(&deps{}); r != (harness.UnavailableRunner{}) || w != (harness.NoopWaker{}) {
		t.Fatal("missing VM must give the unavailable runner")
	}
	srv, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: d.Auth, TenantID: cfg.TenantID, Logger: d.Logger}, c.Routes...)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Stop(ctx)
	// Every harness config now points at the gateway.
	b, err := os.ReadFile(filepath.Join(stor.home, ".codex/config.toml"))
	if err != nil || !strings.Contains(string(b), `base_url = "http://127.0.0.1:3425/v1"`) {
		t.Fatalf("codex config: %s %v", b, err)
	}
	h := srv.Handler()
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/api/v1/models/gateway", 200, `"status":"error"`},
		{"/api/v1/models/providers", 503, `"unavailable"`},
		{"/api/v1/models/bindings", 200, `"configPath":"/home/agent/.codex/config.toml"`},
		{"/api/v1/sessions", 200, `"sessions":[]`},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer test-admin-token-0123456789abcdef")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s = %d %s", tc.path, rec.Code, rec.Body)
		}
	}
}
