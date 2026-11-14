package webrtc

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/pion/interceptor"
	pion "github.com/pion/webrtc/v4"
)

// Signal is one signaling message (docs/api.md section 9): offer, answer,
// ice, or error.
type Signal struct {
	Type      string                 `json:"type"`
	SDP       string                 `json:"sdp,omitempty"`
	Candidate *pion.ICECandidateInit `json:"candidate,omitempty"`
	Code      string                 `json:"code,omitempty"`
	Message   string                 `json:"message,omitempty"`
}

// Peer is one viewer's PeerConnection. Creating it counts a viewer (and
// starts the encoder for the first one); Close releases it.
type Peer struct {
	s    *Streamer
	pc   *pion.PeerConnection
	send func(Signal)

	mu        sync.Mutex
	answered  bool
	pending   []pion.ICECandidateInit
	closeOnce sync.Once
}

// PeerConfig is one viewer's ICE configuration.
type PeerConfig struct {
	// PublicIP, when set, replaces the advertised host candidate address so
	// a browser outside the VM network can reach the forwarded media port.
	PublicIP string
	// ICEServers are the STUN and TURN servers of this viewer. A TURN server
	// gives the guest a relay candidate, which works when the host has no
	// inbound UDP port; STUN gives a server-reflexive candidate.
	ICEServers []pion.ICEServer
	// RelayOnly limits the peer to TURN relay candidates.
	RelayOnly bool
}

// NewPeer creates a viewer. send delivers signaling messages to the browser
// (through the host relay).
func (s *Streamer) NewPeer(cfg PeerConfig, send func(Signal)) (*Peer, error) {
	me := &pion.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := pion.RegisterDefaultInterceptors(me, ir); err != nil {
		return nil, err
	}
	se := pion.SettingEngine{}
	se.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	if s.opts.Net != nil {
		se.SetNet(s.opts.Net)
	}
	if s.mux != nil && !cfg.RelayOnly {
		se.SetICEUDPMux(s.mux)
	}
	if cfg.PublicIP != "" && !cfg.RelayOnly {
		se.SetNAT1To1IPs([]string{cfg.PublicIP}, pion.ICECandidateTypeHost)
	}
	if s.opts.IncludeLoopback {
		se.SetIncludeLoopbackCandidate(true)
	}
	if s.opts.ICETimeouts != nil {
		t := s.opts.ICETimeouts
		se.SetICETimeouts(t.Disconnected, t.Failed, t.KeepAlive)
	}
	conf := pion.Configuration{ICEServers: cfg.ICEServers}
	if cfg.RelayOnly {
		conf.ICETransportPolicy = pion.ICETransportPolicyRelay
	}
	api := pion.NewAPI(pion.WithMediaEngine(me), pion.WithInterceptorRegistry(ir), pion.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(conf)
	if err != nil {
		return nil, fmt.Errorf("webrtc: new peer connection: %w", err)
	}
	p := &Peer{s: s, pc: pc, send: send}
	pc.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		send(Signal{Type: "ice", Candidate: &init})
	})
	pc.OnDataChannel(func(dc *pion.DataChannel) {
		if dc.Label() != "input" {
			return
		}
		dc.OnMessage(func(msg pion.DataChannelMessage) {
			if s.opts.Input == nil {
				return
			}
			if err := s.opts.Input(msg.Data); err != nil {
				s.opts.Logger.Debug("desktop input rejected", "err", err)
			}
		})
	})
	pc.OnConnectionStateChange(func(st pion.PeerConnectionState) {
		s.opts.Logger.Debug("viewer connection state", "state", st.String())
		if st == pion.PeerConnectionStateFailed {
			send(Signal{Type: "error", Code: "unavailable", Message: "WebRTC connection failed"})
		}
	})
	s.acquire()
	return p, nil
}

// HandleSignal processes an offer or a remote ICE candidate from the browser.
func (p *Peer) HandleSignal(raw json.RawMessage) error {
	var m Signal
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("webrtc: invalid signaling message: %w", err)
	}
	switch m.Type {
	case "offer":
		return p.answer(m.SDP)
	case "ice":
		if m.Candidate == nil {
			return nil
		}
		p.mu.Lock()
		if !p.answered {
			p.pending = append(p.pending, *m.Candidate)
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
		return p.pc.AddICECandidate(*m.Candidate)
	}
	return fmt.Errorf("webrtc: unexpected signaling message %q", m.Type)
}

func (p *Peer) answer(sdp string) error {
	if err := p.pc.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: sdp}); err != nil {
		return fmt.Errorf("webrtc: set offer: %w", err)
	}
	// AddTrack reuses the browser's recvonly video transceiver.
	sender, err := p.pc.AddTrack(p.s.track)
	if err != nil {
		return fmt.Errorf("webrtc: add track: %w", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	ans, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("webrtc: create answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(ans); err != nil {
		return fmt.Errorf("webrtc: set answer: %w", err)
	}
	p.send(Signal{Type: "answer", SDP: ans.SDP})
	p.mu.Lock()
	p.answered = true
	pending := p.pending
	p.pending = nil
	p.mu.Unlock()
	for _, c := range pending {
		if err := p.pc.AddICECandidate(c); err != nil {
			return err
		}
	}
	return nil
}

// ConnectionState returns the PeerConnection state.
func (p *Peer) ConnectionState() pion.PeerConnectionState { return p.pc.ConnectionState() }

// SelectedPair returns the local and remote candidate types of the selected
// ICE candidate pair ("" while none is selected).
func (p *Peer) SelectedPair() (local, remote string) {
	sctp := p.pc.SCTP()
	if sctp == nil || sctp.Transport() == nil || sctp.Transport().ICETransport() == nil {
		return "", ""
	}
	pair, err := sctp.Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil {
		return "", ""
	}
	return pair.Local.Typ.String(), pair.Remote.Typ.String()
}

// Close closes the PeerConnection and releases the viewer.
func (p *Peer) Close() error {
	var err error
	p.closeOnce.Do(func() {
		err = p.pc.Close()
		p.s.release()
	})
	return err
}
