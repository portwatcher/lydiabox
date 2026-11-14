package cli

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/cloudflare"
	"github.com/lydiabox/lydiabox/internal/cloudflare/accessjwt"
	"github.com/lydiabox/lydiabox/internal/cloudflare/cloudflared"
	"github.com/lydiabox/lydiabox/internal/config"
)

// remoteAccess is the remote access state of a running control plane.
type remoteAccess struct {
	state     cloudflare.State
	access    *accessjwt.Verifier
	tunnel    *cloudflared.Supervisor
	turnInUse bool
}

// RemoteAccessStatus implements api.RemoteAccessSource.
func (ra *remoteAccess) RemoteAccessStatus(_ context.Context, _ string) api.RemoteAccessStatus {
	st := api.RemoteAccessStatus{Provider: remoteProvider, Configured: ra.access != nil,
		Hostname: ra.state.Hostname, ServiceTokenClientID: ra.state.ServiceTokenClientID, BypassPaths: ra.state.BypassPaths,
		TURNKeyID: ra.state.TURNKeyID, TURNInUse: ra.turnInUse}
	if ra.access != nil {
		st.TeamDomain = ra.access.TeamDomain()
		st.Emails = ra.accessEmails()
	}
	if ra.tunnel != nil {
		s := ra.tunnel.Status()
		st.Tunnel = &api.TunnelStatus{State: s.State, LastError: s.LastError, Restarts: s.Restarts, StartedAt: s.StartedAt}
	}
	return st
}

func (ra *remoteAccess) accessEmails() []string { return ra.state.Emails }

// accessSettings merges the stored Access settings with the
// LYDIABOX_ACCESS_* overrides. ok is false when Access login is off.
func accessSettings(r config.RemoteAccessConfig, st cloudflare.State) (team, aud string, emails []string, ok bool, err error) {
	team, aud, emails = st.TeamDomain, st.AUD, st.Emails
	if r.AccessTeamDomain != "" {
		team = r.AccessTeamDomain
	}
	if r.AccessAUD != "" {
		aud = r.AccessAUD
	}
	if len(r.AccessEmails) > 0 {
		emails = r.AccessEmails
	}
	if team == "" && aud == "" && len(emails) == 0 {
		return "", "", nil, false, nil
	}
	var missing []string
	if team == "" {
		missing = append(missing, "LYDIABOX_ACCESS_TEAM_DOMAIN")
	}
	if aud == "" {
		missing = append(missing, "LYDIABOX_ACCESS_AUD")
	}
	if len(emails) == 0 {
		missing = append(missing, "LYDIABOX_ACCESS_EMAILS")
	}
	if len(missing) > 0 {
		return "", "", nil, false, fmt.Errorf("the Cloudflare Access settings are incomplete: set %s or run 'lydiabox cloudflare setup'",
			strings.Join(missing, ", "))
	}
	return team, aud, emails, true, nil
}

// wireRemote wires remote access through Cloudflare Zero Trust into
// `lydiabox up`: Access login on the API, the cloudflared connector, and
// GET /api/v1/remote-access. It runs first among the wirers because an
// active tunnel moves the default listen address to loopback.
func wireRemote(ctx context.Context, d *deps) (*component, error) {
	cfg := d.Config
	log := d.Logger.With("component", "remote-access")
	state, sec, stored, err := loadCloudflare(ctx, d.Store, cfg.TenantID)
	if err != nil {
		return nil, err
	}
	ra := &remoteAccess{state: state}
	comp := &component{Name: "remote access", Routes: []api.RouteRegistrar{api.RemoteAccessRoutes{Source: ra}}}

	ra.turnInUse = applyStoredTURN(d, state, sec, log)

	team, aud, emails, on, err := accessSettings(cfg.Remote, state)
	if err != nil {
		return nil, err
	}
	if on {
		v, err := accessjwt.New(accessjwt.Config{TeamDomain: team, AUD: aud, Emails: emails})
		if err != nil {
			return nil, err
		}
		ra.access = v
		ra.state.Emails = slices.Clone(emails)
		d.Auth.EnableAccess(v, d.Logger)
		log.Info("Cloudflare Access login is on", "teamDomain", v.TeamDomain(), "emails", len(emails))
	}

	runTunnel := stored && sec.TunnelToken != "" && cfg.Remote.Tunnel == config.TunnelAuto
	switch {
	case runTunnel && !cfg.Remote.ListenAddrSet:
		cfg.ListenAddr = config.LoopbackListenAddr(cfg.ListenAddr)
		log.Info("the tunnel is on; listening on loopback only (set LYDIABOX_LISTEN_ADDR to change this)", "addr", cfg.ListenAddr)
	case on && !config.IsLoopbackAddr(cfg.ListenAddr):
		log.Warn("the listener is reachable without Cloudflare Access; only the admin token protects it there",
			"addr", cfg.ListenAddr)
	}
	if !runTunnel {
		return comp, nil
	}
	if state.OriginURL != "" && !originMatches(state.OriginURL, cfg.ListenAddr) {
		log.Warn("the tunnel sends requests to another port; run 'lydiabox cloudflare setup' again",
			"origin", state.OriginURL, "listen", cfg.ListenAddr)
	}
	tunnel, err := newTunnel(ctx, d, sec.TunnelToken, log)
	if err != nil {
		return nil, err
	}
	ra.tunnel = tunnel
	comp.Start = tunnel.Start
	comp.Stop = tunnel.Stop
	return comp, nil
}

// applyStoredTURN makes the desktop stream use the TURN key of
// `lydiabox cloudflare setup` (relay only, no UDP port forward) when no
// WebRTC setting is set. Explicit settings always win.
func applyStoredTURN(d *deps, state cloudflare.State, sec cloudflare.Secrets, log *slog.Logger) bool {
	if state.TURNKeyID == "" || sec.TURNKeySecret == "" {
		return false
	}
	w := &d.Config.WebRTC
	if w.Configured() {
		log.Info("WebRTC settings are set; the stored Cloudflare TURN key is not used")
		return false
	}
	w.CloudflareTURNKeyID, w.CloudflareTURNAPIToken = state.TURNKeyID, sec.TURNKeySecret
	if !w.RelayOnlySet {
		w.RelayOnly = true
		d.StoredTURN = true
	}
	log.Info("the desktop stream uses the stored Cloudflare TURN key", "relayOnly", w.RelayOnly)
	return true
}

// newTunnel resolves (and on first use installs) cloudflared and returns
// its supervisor.
func newTunnel(ctx context.Context, d *deps, token string, log *slog.Logger) (*cloudflared.Supervisor, error) {
	opts := cloudflared.Options{TenantID: d.Config.TenantID, Token: token, DataDir: d.Config.CloudflaredDir(), Logger: d.Logger}
	bin, err := d.Bootstrap.Resolve("cloudflared")
	if err != nil && !d.SkipBootstrap {
		log.Info("installing cloudflared for the tunnel")
		if ierr := d.Bootstrap.Install(ctx, "cloudflared"); ierr != nil {
			log.Warn("cloudflared install failed", "err", ierr)
		} else {
			bin, err = d.Bootstrap.Resolve("cloudflared")
		}
	}
	if err != nil {
		opts.BinaryError = err.Error()
	} else {
		opts.Binary = bin
	}
	return cloudflared.NewSupervisor(opts)
}
