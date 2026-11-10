package cli

import (
	"context"
	"net/http"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/desktop"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// wireDesktop serves /api/v1/desktop/* (signaling relay, takeover mode,
// agent_action broadcast) on top of the VM subsystem from wireVM.
func wireDesktop(ctx context.Context, d *deps) (*component, error) {
	if d.VM == nil || d.Guest == nil {
		return nil, nil
	}
	ctl := d.VM.Controller
	// A chv guest is reachable from the host over its TAP address, so a
	// browser on the host itself uses the guest's own ICE candidate; the
	// docker driver publishes the UDP port on the host instead.
	keepLoopback := d.Driver != config.DriverCHV
	relay, err := desktop.New(desktop.Options{
		Guest:    desktop.GuestAdapter{Client: d.Guest},
		Power:    ctl,
		Running:  func(tid string) bool { return ctl.State(tid) == vmm.StateRunning },
		PublicIP: func(r *http.Request) string { return desktop.RequestIP(r, keepLoopback) },
		Logger:   d.Logger.With("component", "desktop"),
	})
	if err != nil {
		return nil, err
	}
	return &component{Name: "desktop", Routes: []api.RouteRegistrar{api.DesktopRoutes{Relay: relay}}}, nil
}
