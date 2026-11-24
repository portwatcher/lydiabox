package cli

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/agentfs"
	"github.com/portwatcher/lydiabox/internal/agentfs/remotefs"
	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/bootstrap"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/storage"
	"github.com/portwatcher/lydiabox/internal/storage/files"
	"github.com/portwatcher/lydiabox/internal/store"
	"github.com/portwatcher/lydiabox/internal/vmm"
)

// TestProviderModeWiring wires `lydiabox up` in the provider storage mode
// without S3 settings: no juicefs, no object store, the guest-backed file
// system, the cached session list, and 501 on /webdav and /s3. Nothing
// wakes the machine.
func TestProviderModeWiring(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	cfg := config.Defaults(base)
	cfg.DataDir = base
	cfg.Storage.Mode = config.StorageProvider
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("provider mode without S3: %v", err)
	}
	driver := cfg.EffectiveVMDriver()
	if driver != config.DriverDocker {
		t.Fatalf("driver = %q", driver)
	}
	if objs, err := objectStore(cfg); objs != nil || err != nil {
		t.Fatalf("object store = %v, %v", objs, err)
	}
	if !slices.Contains(skipBinaries(cfg), "juicefs") {
		t.Fatal("juicefs must not be installed in the provider mode")
	}
	if slices.Contains(skipBinaries(cfg), "magpie") {
		t.Fatal("the self-hosted mode needs magpie")
	}
	native := true
	cfg.GuestNativeSet = &native
	if !slices.Contains(skipBinaries(cfg), "magpie") {
		t.Fatal("magpie must not be installed in guest-native mode")
	}
	cfg.GuestNativeSet = nil
	boot, err := bootstrap.New(bootstrap.Options{BinDir: filepath.Join(base, "bin"), Driver: driver, Skip: skipBinaries(cfg),
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") }})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := boot.Check()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range statuses {
		if s.Name == "juicefs" && s.Required {
			t.Fatal("juicefs is required in the provider mode")
		}
	}
	st, err := store.Open(ctx, cfg.DBPath(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.DiscardHandler)
	stor, err := newStorageRuntime(cfg, driver, boot, nil, st, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stor.Provider.(*storage.StateBackup); !ok || stor.Files != nil || stor.Gateway != nil {
		t.Fatalf("storage runtime = %+v", stor)
	}

	d := &deps{Config: cfg, Driver: driver, Logger: log, Store: st, Bootstrap: boot, Storage: stor.Provider, Files: stor.Files,
		Auth: api.NewAuthenticator("test-admin-token-0123456789abcdef"), VMStatus: api.StaticVMStatus{State: api.VMStopped, Driver: driver}}
	vmc, err := wireVM(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Files.(*remotefs.FS); !ok {
		t.Fatalf("files = %T, want *remotefs.FS", d.Files)
	}
	elf := filepath.Join(base, "lydiabox-guest")
	if err := os.WriteFile(elf, []byte("\x7fELF\x02\x01"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envGuestBinary, elf)
	spec, err := vmSpec(ctx, d, cfg.TenantID, 0)
	if err != nil || spec.HomeDir != "" || spec.WorkspaceDir != "" || spec.RootUpper != "" {
		t.Fatalf("spec = %+v, %v", spec, err)
	}
	hc, err := wireHarness(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := wireConnector(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := hc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer hc.Stop(ctx)

	regs := []api.RouteRegistrar{
		api.CoreRoutes{Auth: d.Auth, VM: d.VMStatus, Storage: stor.Provider, S3Gateway: stor.Gateway},
		api.FilesRoutes{Handler: files.NewHandler(files.NewService(d.Files, files.Options{}))},
	}
	regs = append(regs, stor.Routes...)
	for _, c := range []*component{vmc, hc, cc} {
		regs = append(regs, c.Routes...)
	}
	srv, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: d.Auth, TenantID: cfg.TenantID, Logger: log}, regs...)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	for _, tc := range []struct {
		path string
		auth bool
		code int
		body string
	}{
		{"/webdav/", true, 501, "not available in the provider storage mode"},
		{"/s3/workspace/a.txt", false, 501, "not available in the provider storage mode"},
		{"/api/v1/status", true, 200, `"mode":"provider"`},
		{"/api/v1/sessions", true, 200, `"cached":true`},
		{"/api/v1/fs/roots", true, 200, `"guestPath":"/home/agent"`},
		{"/api/v1/fs/workspace/list?path=..%2Fx", true, 400, "bad_request"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.auth {
			req.Header.Set("Authorization", "Bearer test-admin-token-0123456789abcdef")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s = %d %s", tc.path, rec.Code, rec.Body)
		}
	}
	shutdown := newShutdown(log)
	if err := stor.start(ctx, shutdown); err != nil {
		t.Fatal(err)
	}
	shutdown.run()
	if got := d.VM.Controller.State(cfg.TenantID); got != vmm.StateStopped {
		t.Fatalf("the machine state changed to %s", got)
	}
	if err := d.Files.MkdirAll(ctx, cfg.TenantID, "etc", "x"); !errors.Is(err, agentfs.ErrUnknownRoot) {
		t.Fatalf("unknown root = %v", err)
	}
}
