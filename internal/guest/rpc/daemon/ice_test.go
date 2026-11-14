package daemon

import (
	"encoding/json"
	"testing"

	pion "github.com/pion/webrtc/v4"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

func TestPeerConfigFromSignalParams(t *testing.T) {
	var p rpc.SignalParams
	raw := `{"publicIp":"203.0.113.7","iceTransportPolicy":"relay","iceServers":[
{"urls":["stun:stun.cloudflare.com:3478"]},
{"urls":["turns:turn.cloudflare.com:443?transport=tcp"],"username":"u","credential":"c"}]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	cfg := peerConfig(p)
	if !cfg.RelayOnly || cfg.PublicIP != "203.0.113.7" || len(cfg.ICEServers) != 2 {
		t.Fatalf("config %+v", cfg)
	}
	turn := cfg.ICEServers[1]
	if turn.Username != "u" || turn.Credential != "c" || turn.CredentialType != pion.ICECredentialTypePassword ||
		turn.URLs[0] != "turns:turn.cloudflare.com:443?transport=tcp" {
		t.Fatalf("turn %+v", turn)
	}
	if def := peerConfig(rpc.SignalParams{PublicIP: "198.51.100.1"}); def.RelayOnly || def.ICEServers != nil {
		t.Fatalf("default %+v", def)
	}
}
