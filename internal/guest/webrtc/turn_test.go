package webrtc

import (
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
)

// The guest is behind the host NAT with no inbound port, and the browser is
// behind its own NAT; both NATs are symmetric. With the relay-only policy on
// both peers, video RTP and DataChannel input flow through the TURN server.
func TestRelayOnlyThroughTURNBehindNAT(t *testing.T) {
	w := newNATWorld(t)
	srv := turnServer(t, "turn:"+wanTURNIP+":3478?transport=udp")
	r := newRig(t, rigConfig{guestNet: w.guest, browserNet: w.brows,
		guest:   PeerConfig{ICEServers: []pion.ICEServer{srv}, RelayOnly: true},
		browser: pion.Configuration{ICEServers: []pion.ICEServer{srv}, ICETransportPolicy: pion.ICETransportPolicyRelay}})
	r.expectMedia(t)
	g, b := r.pairs(t)
	if g != "relay/relay" || b != "relay/relay" {
		t.Fatalf("selected pairs: guest %s, browser %s", g, b)
	}
	seen := map[string]bool{}
	for len(w.allocations) > 0 {
		seen[(<-w.allocations)[:8]] = true
	}
	if !seen["27.1.1.1"] || !seen["28.1.1.1"] {
		t.Fatalf("TURN clients %v: want the guest NAT 27.1.1.1 and the browser NAT 28.1.1.1", seen)
	}
}

// The guest relay candidate alone is enough: a browser without TURN, behind
// a symmetric NAT, reaches the guest's relayed address directly.
func TestGuestRelayCandidateWithoutBrowserTURN(t *testing.T) {
	w := newNATWorld(t)
	stun := pion.ICEServer{URLs: []string{"stun:" + wanTURNIP + ":3478"}}
	r := newRig(t, rigConfig{guestNet: w.guest, browserNet: w.brows,
		guest:   PeerConfig{ICEServers: []pion.ICEServer{stun, turnServer(t, "turn:"+wanTURNIP+":3478")}},
		browser: pion.Configuration{ICEServers: []pion.ICEServer{stun}}})
	r.expectMedia(t)
	if g, _ := r.pairs(t); g[:6] != "relay/" {
		t.Fatalf("guest selected pair %s, want a relay local candidate", g)
	}
}

// Control: with STUN only (the previous guest setup), two symmetric NATs
// give no working candidate pair.
func TestNoTURNFailsBehindNAT(t *testing.T) {
	w := newNATWorld(t)
	stun := pion.ICEServer{URLs: []string{"stun:" + wanTURNIP + ":3478"}}
	r := newRig(t, rigConfig{guestNet: w.guest, browserNet: w.brows,
		guest:   PeerConfig{ICEServers: []pion.ICEServer{stun}},
		browser: pion.Configuration{ICEServers: []pion.ICEServer{stun}}})
	select {
	case <-r.opened:
		g, b := r.pairs(t)
		t.Fatalf("connected without TURN: guest %s, browser %s", g, b)
	case <-time.After(6 * time.Second):
	}
}

// The browser's relay is enough when the guest has STUN: the guest's
// server-reflexive address gets a TURN permission on the browser's relay,
// and the guest's checks arrive as peer-reflexive candidates. The guest
// gets TURN as well, for the relay-only policy, for a browser without
// TURN, and for a host that blocks outbound UDP (TURN over TCP or TLS).
func TestGuestServerReflexiveWithBrowserRelay(t *testing.T) {
	w := newNATWorld(t)
	stun := pion.ICEServer{URLs: []string{"stun:" + wanTURNIP + ":3478"}}
	r := newRig(t, rigConfig{guestNet: w.guest, browserNet: w.brows,
		guest: PeerConfig{ICEServers: []pion.ICEServer{stun}},
		browser: pion.Configuration{ICEServers: []pion.ICEServer{turnServer(t, "turn:"+wanTURNIP+":3478")},
			ICETransportPolicy: pion.ICETransportPolicyRelay}})
	r.expectMedia(t)
	if _, b := r.pairs(t); b[:6] != "relay/" {
		t.Fatalf("browser selected pair %s, want a relay local candidate", b)
	}
}
