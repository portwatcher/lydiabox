package cli

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
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
	t.Setenv("LYDIABOX_WEBRTC_UDP_PORT", "")
	if p, err := webRTCPort(defaultWebRTCPort(&deps{})); err != nil || p != 50000 {
		t.Fatalf("default port %d %v", p, err)
	}
	// The stored Cloudflare TURN key turns the default forward off.
	if p, err := webRTCPort(defaultWebRTCPort(&deps{StoredTURN: true})); err != nil || p != 0 {
		t.Fatalf("stored TURN port %d %v", p, err)
	}
	t.Setenv("LYDIABOX_WEBRTC_UDP_PORT", "50001")
	if p, _ := webRTCPort(defaultWebRTCPort(&deps{StoredTURN: true})); p != 50001 {
		t.Fatalf("an explicit port must win over the stored TURN key, got %d", p)
	}
	t.Setenv("LYDIABOX_WEBRTC_UDP_PORT", "70000")
	if _, err := webRTCPort(rpc.DefaultWebRTCPort); err == nil {
		t.Fatal("invalid port accepted")
	}
	dir := t.TempDir()
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
