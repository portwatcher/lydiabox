package daemon

import (
	pion "github.com/pion/webrtc/v4"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/webrtc"
)

// peerConfig converts the signaling parameters from the host into the ICE
// configuration of one viewer. The TURN credentials stay in memory: they
// are not logged and not written to the guest disk.
func peerConfig(p rpc.SignalParams) webrtc.PeerConfig {
	cfg := webrtc.PeerConfig{PublicIP: p.PublicIP, RelayOnly: p.ICETransportPolicy == rpc.ICEPolicyRelay}
	for _, s := range p.ICEServers {
		srv := pion.ICEServer{URLs: append([]string(nil), s.URLs...), Username: s.Username}
		if s.Credential != "" {
			srv.Credential = s.Credential
			srv.CredentialType = pion.ICECredentialTypePassword
		}
		cfg.ICEServers = append(cfg.ICEServers, srv)
	}
	return cfg
}
