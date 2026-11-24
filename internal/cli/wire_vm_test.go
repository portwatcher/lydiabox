package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

func TestWireVMAndDesktopWithDockerDriver(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	d := &deps{Config: cfg, Driver: config.DriverDocker, Logger: slog.New(slog.DiscardHandler),
		VMStatus: api.StaticVMStatus{State: api.VMStopped, Driver: config.DriverDocker}}
	vm, err := wireVM(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if vm == nil || len(vm.Routes) != 1 || d.VM == nil || d.Guest == nil || d.VM.Gateway == nil {
		t.Fatalf("vm component %+v deps %+v", vm, d)
	}
	st, err := d.VMStatus.VMStatus(context.Background(), cfg.TenantID)
	if err != nil || st.State != api.VMStopped || st.Driver != "docker" {
		t.Fatalf("status %+v %v", st, err)
	}
	desk, err := wireDesktop(context.Background(), d)
	if err != nil || desk == nil || len(desk.Routes) != 1 {
		t.Fatalf("desktop component %+v %v", desk, err)
	}
	if c, _ := wireDesktop(context.Background(), &deps{}); c != nil {
		t.Fatal("desktop wired without a VM")
	}
}

func TestWebRTCPortAndGuestBinary(t *testing.T) {
	dir := t.TempDir()
	load := func(port string) *config.Config {
		t.Helper()
		cfg, err := config.Load(config.LoadOptions{HomeDir: dir, ConfigFile: os.DevNull,
			Environ: func() []string { return []string{"LYDIABOX_WEBRTC_UDP_PORT=" + port} }})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if p := webRTCPort(&deps{Config: load("")}); p != rpc.DefaultWebRTCPort {
		t.Fatalf("default port %d", p)
	}
	// The stored Cloudflare TURN key turns the default forward off.
	if p := webRTCPort(&deps{Config: load(""), StoredTURN: true}); p != 0 {
		t.Fatalf("stored TURN port %d", p)
	}
	if p := webRTCPort(&deps{Config: load("50001"), StoredTURN: true}); p != 50001 {
		t.Fatalf("an explicit port must win over the stored TURN key, got %d", p)
	}
	if p := webRTCPort(&deps{Config: load("0")}); p != 0 {
		t.Fatalf("an explicit 0 must disable the forward, got %d", p)
	}
	if err := load("70000").Validate(); err == nil {
		t.Fatal("invalid port accepted")
	}
	elf := filepath.Join(dir, "guest")
	if err := os.WriteFile(elf, []byte("\x7fELF\x02\x01"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envGuestBinary, elf)
	cfg := config.Defaults(dir)
	if got, err := guestBinary(cfg); err != nil || got != elf {
		t.Fatalf("guest binary %q %v", got, err)
	}
	if isELF(filepath.Join(dir, "missing")) {
		t.Fatal("missing file is ELF")
	}
}
