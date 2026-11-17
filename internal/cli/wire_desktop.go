package cli

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/desktop"
	"github.com/lydiabox/lydiabox/internal/desktop/ice"
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
	log := d.Logger.With("component", "desktop")
	iceProvider, err := desktopICE(d.Config.WebRTC, log)
	if err != nil {
		return nil, err
	}
	relay, err := desktop.New(desktop.Options{
		Guest:     desktop.GuestAdapter{Client: d.Guest},
		Power:     ctl,
		Running:   func(tid string) bool { return ctl.State(tid) == vmm.StateRunning },
		Stopped:   ctl.Held,
		Paused:    ctl.Paused,
		PublicIP:  func(r *http.Request) string { return desktop.RequestIP(r, keepLoopback) },
		ICE:       iceProvider,
		RelayOnly: d.Config.WebRTC.RelayOnly,
		Logger:    log,
	})
	if err != nil {
		return nil, err
	}
	// A pause from any source ends the open desktop streams at once with
	// "paused"; the pages show the hibernating state and do not reconnect.
	if d.VM.hooks != nil {
		d.VM.OnState(func(tid string, st vmm.State) {
			if st == vmm.StatePaused {
				relay.EndPaused(tid)
			}
		})
	}
	return &component{Name: "desktop", Routes: []api.RouteRegistrar{api.DesktopRoutes{Relay: relay}}}, nil
}

// desktopICE returns the ICE provider of the WebRTC settings, or nil when
// nothing is configured (the browser gets the default STUN server and the
// guest none).
func desktopICE(w config.WebRTCConfig, log *slog.Logger) (ice.Provider, error) {
	if !w.Configured() {
		return nil, nil
	}
	cfg := ice.Config{
		STUNURLs: w.STUNURLs, TURNURLs: w.TURNURLs, TURNUsername: w.TURNUsername, TURNPassword: w.TURNPassword,
		TURNSecret: w.TURNSecret, TTL: w.TURNTTL, Logger: log,
	}
	if w.CloudflareTURNKeyID != "" {
		cfg.Cloudflare = &ice.Cloudflare{KeyID: w.CloudflareTURNKeyID, APIToken: w.CloudflareTURNAPIToken}
	}
	return ice.New(cfg)
}
