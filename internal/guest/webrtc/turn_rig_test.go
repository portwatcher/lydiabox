package webrtc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/v3/vnet"
	"github.com/pion/turn/v4"
	pion "github.com/pion/webrtc/v4"
)

const (
	turnSecret = "turn-test-secret"
	turnRealm  = "lydiabox.test"
	wanTURNIP  = "1.2.3.4"
)

// symmetricNAT maps and filters by destination address and port: a peer
// behind it receives nothing it did not send to first, and its STUN-mapped
// port differs from the port that other destinations see. This is the worst
// case for a host without an inbound UDP port.
var symmetricNAT = &vnet.NATType{Mode: vnet.NATModeNormal,
	MappingBehavior: vnet.EndpointAddrPortDependent, FilteringBehavior: vnet.EndpointAddrPortDependent}

// natWorld is a virtual internet: a TURN server at 1.2.3.4:3478, the guest
// behind one NAT (public 27.1.1.1), and the browser behind another (public
// 28.1.1.1).
type natWorld struct {
	wan          *vnet.Router
	guest, brows *vnet.Net
	server       *turn.Server
	allocations  chan string
}

func newNATWorld(t *testing.T) *natWorld {
	t.Helper()
	lf := logging.NewDefaultLoggerFactory()
	lf.DefaultLogLevel = logging.LogLevelDisabled
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: lf})
	must(t, err)
	wanNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{wanTURNIP}})
	must(t, err)
	must(t, wan.AddNet(wanNet))
	lan := func(public, cidr, ip string) *vnet.Net {
		r, err := vnet.NewRouter(&vnet.RouterConfig{StaticIPs: []string{public}, CIDR: cidr, NATType: symmetricNAT, LoggerFactory: lf})
		must(t, err)
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
		must(t, err)
		must(t, r.AddNet(n))
		must(t, wan.AddRouter(r))
		return n
	}
	w := &natWorld{wan: wan, guest: lan("27.1.1.1", "10.1.0.0/24", "10.1.0.2"),
		brows: lan("28.1.1.1", "10.2.0.0/24", "10.2.0.2"), allocations: make(chan string, 64)}
	must(t, wan.Start())
	pc, err := wanNet.ListenPacket("udp4", wanTURNIP+":3478")
	must(t, err)
	w.server, err = turn.NewServer(turn.ServerConfig{
		Realm: turnRealm, AuthHandler: w.auth(turn.LongTermTURNRESTAuthHandler(turnSecret, nil)), LoggerFactory: lf,
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: pc,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP(wanTURNIP), Address: "0.0.0.0", Net: wanNet}}},
	})
	must(t, err)
	t.Cleanup(func() {
		_ = w.server.Close()
		_ = wan.Stop()
	})
	return w
}

// auth wraps a TURN auth handler and records each authenticated client
// address.
func (w *natWorld) auth(h turn.AuthHandler) turn.AuthHandler {
	return func(user, realm string, src net.Addr) ([]byte, bool) {
		key, ok := h(user, realm, src)
		if ok {
			select {
			case w.allocations <- src.String():
			default:
			}
		}
		return key, ok
	}
}

// turnServer returns a TURN server entry with short-lived REST credentials.
func turnServer(t *testing.T, urls ...string) pion.ICEServer {
	t.Helper()
	user, pass, err := turn.GenerateLongTermTURNRESTCredentials(turnSecret, "default", 10*time.Minute)
	must(t, err)
	return pion.ICEServer{URLs: urls, Username: user, Credential: pass, CredentialType: pion.ICECredentialTypePassword}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// rig is a streamer, one guest peer, and a pion "browser" peer that sends
// an offer with a recvonly H.264 transceiver and the input data channel.
type rig struct {
	s        *Streamer
	peer     *Peer
	browser  *pion.PeerConnection
	dc       *pion.DataChannel
	inputs   chan string
	gotVideo chan string
	opened   chan struct{}
}

type rigConfig struct {
	guestNet, browserNet *vnet.Net
	guest                PeerConfig
	browser              pion.Configuration
	loopback             bool
	// encoder replaces the fake encoder; stream changes the options.
	encoder Launcher
	stream  func(*Options)
	// onTrack replaces the default video reader of the browser.
	onTrack func(*pion.TrackRemote)
}

var fastICE = &ICETimeouts{Disconnected: 2 * time.Second, Failed: 4 * time.Second, KeepAlive: 300 * time.Millisecond}

func newRig(t *testing.T, cfg rigConfig) *rig {
	t.Helper()
	r := &rig{inputs: make(chan string, 16), gotVideo: make(chan string, 1), opened: make(chan struct{})}
	opts := Options{RTPAddr: "127.0.0.1:0", Encoder: &fakeEncoder{}, IncludeLoopback: cfg.loopback, ICETimeouts: fastICE,
		Logger: slog.New(slog.DiscardHandler), Input: func(b []byte) error { r.inputs <- string(b); return nil }}
	if cfg.guestNet != nil {
		opts.Net = cfg.guestNet
	}
	if cfg.encoder != nil {
		opts.Encoder = cfg.encoder
	}
	if cfg.stream != nil {
		cfg.stream(&opts)
	}
	s, err := New(opts)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	r.s = s

	se := pion.SettingEngine{}
	se.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	se.SetIncludeLoopbackCandidate(cfg.loopback)
	se.SetICETimeouts(fastICE.Disconnected, fastICE.Failed, fastICE.KeepAlive)
	if cfg.browserNet != nil {
		se.SetNet(cfg.browserNet)
	}
	me := &pion.MediaEngine{}
	must(t, me.RegisterDefaultCodecs())
	r.browser, err = pion.NewAPI(pion.WithMediaEngine(me), pion.WithSettingEngine(se)).NewPeerConnection(cfg.browser)
	must(t, err)
	t.Cleanup(func() { _ = r.browser.Close() })
	_, err = r.browser.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly})
	must(t, err)
	r.dc, err = r.browser.CreateDataChannel("input", nil)
	must(t, err)
	r.dc.OnOpen(func() { close(r.opened) })
	r.browser.OnTrack(func(tr *pion.TrackRemote, _ *pion.RTPReceiver) {
		if cfg.onTrack != nil {
			cfg.onTrack(tr)
			return
		}
		if _, _, err := tr.ReadRTP(); err == nil {
			r.gotVideo <- tr.Codec().MimeType
		}
	})

	toBrowser := make(chan Signal, 64)
	r.peer, err = s.NewPeer(cfg.guest, func(m Signal) { toBrowser <- m })
	must(t, err)
	t.Cleanup(func() { _ = r.peer.Close() })
	r.browser.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		b, _ := json.Marshal(Signal{Type: "ice", Candidate: &init})
		_ = r.peer.HandleSignal(b)
	})
	offer, err := r.browser.CreateOffer(nil)
	must(t, err)
	must(t, r.browser.SetLocalDescription(offer))
	b, _ := json.Marshal(Signal{Type: "offer", SDP: offer.SDP})
	must(t, r.peer.HandleSignal(b))
	go func() {
		for m := range toBrowser {
			switch m.Type {
			case "answer":
				_ = r.browser.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: m.SDP})
			case "ice":
				_ = r.browser.AddICECandidate(*m.Candidate)
			}
		}
	}()
	return r
}

// expectMedia waits for the input data channel and video RTP, and checks
// that a takeover input message reaches the guest.
func (r *rig) expectMedia(t *testing.T) {
	t.Helper()
	select {
	case <-r.opened:
	case <-time.After(30 * time.Second):
		t.Fatalf("data channel did not open (guest %s, browser %s)", r.peer.ConnectionState(), r.browser.ConnectionState())
	}
	msg := `{"t":"keydown","code":"KeyA","key":"a"}`
	must(t, r.dc.SendText(msg))
	select {
	case in := <-r.inputs:
		if in != msg {
			t.Fatalf("input %q", in)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("input not delivered")
	}
	select {
	case mime := <-r.gotVideo:
		if mime != pion.MimeTypeH264 {
			t.Fatalf("codec %s", mime)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no video RTP received")
	}
}

// pairs returns the selected candidate pair types as "local/remote" on the
// guest and on the browser.
func (r *rig) pairs(t *testing.T) (guest, browser string) {
	t.Helper()
	l, rm := r.peer.SelectedPair()
	bp, err := r.browser.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || bp == nil {
		t.Fatalf("browser has no selected pair: %v", err)
	}
	return fmt.Sprintf("%s/%s", l, rm), fmt.Sprintf("%s/%s", bp.Local.Typ, bp.Remote.Typ)
}
