package cli

import (
	"errors"
	"log/slog"

	"github.com/lydiabox/lydiabox/internal/config"
	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

// instaboxOptions lets a test shorten the timers of the instabox driver.
var instaboxOptions func(*vmmib.Options)

// newInstaboxClient returns the InstaBox API client of the configuration.
// The API key goes only into the X-API-Key header.
func newInstaboxClient(cfg *config.Config, log *slog.Logger) (*ib.Client, error) {
	return ib.NewClient(ib.Options{BaseURL: cfg.Instabox.APIURL, APIKey: cfg.Instabox.APIKey, Logger: log})
}

// newInstaboxDriver builds the instabox VM driver: the machine of the
// tenant on InstaBox, reached over the guest link.
func newInstaboxDriver(d *deps) (vmm.Driver, error) {
	cfg := d.Config
	if d.Store == nil {
		return nil, errors.New("the instabox driver needs the state database")
	}
	client, err := newInstaboxClient(cfg, d.Logger.With("component", "instabox-api"))
	if err != nil {
		return nil, err
	}
	log := d.Logger.With("component", "instabox")
	switch {
	case !cfg.DesktopRelayConfigured() && !d.StoredTURN:
		log.Warn("the desktop stream cannot connect: an InstaBox machine gets no inbound UDP. " +
			"Set a TURN relay with TCP or TLS on port 443 (LYDIABOX_CLOUDFLARE_TURN_KEY_ID and _API_TOKEN, " +
			"LYDIABOX_TURN_URLS, or 'lydiabox cloudflare setup') and LYDIABOX_WEBRTC_RELAY_ONLY=true")
	case !cfg.WebRTC.RelayOnly:
		log.Warn("set LYDIABOX_WEBRTC_RELAY_ONLY=true with the instabox driver: the machine's direct UDP candidates cannot connect")
	}
	opts := vmmib.Options{
		Client: client, Store: d.Store, Snapshot: cfg.Instabox.Snapshot, IdlePause: cfg.Instabox.IdlePause, Logger: log,
	}
	// The driver installs this guest binary on machines that have another
	// one (docs/instabox.md, "Guest binary upgrade").
	if bin, err := guestBinary(cfg); err == nil {
		opts.GuestBinary = bin
	} else {
		log.Info("no guest binary upgrade: " + err.Error())
	}
	if instaboxOptions != nil {
		instaboxOptions(&opts)
	}
	return vmmib.New(opts)
}
