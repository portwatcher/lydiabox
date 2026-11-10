package webrtc

import (
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	pion "github.com/pion/webrtc/v4"
)

// fakeEncoder sends H.264-like RTP packets to the rtp:// URL in its args.
type fakeEncoder struct {
	mu     sync.Mutex
	starts [][]string
}

type fakeProc struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Stop() error {
	p.once.Do(func() { close(p.stop) })
	<-p.done
	return nil
}

func (f *fakeEncoder) Start(args []string) (Process, error) {
	f.mu.Lock()
	f.starts = append(f.starts, args)
	f.mu.Unlock()
	target := strings.TrimPrefix(args[len(args)-1], "rtp://")
	conn, err := net.Dial("udp", target)
	if err != nil {
		return nil, err
	}
	p := &fakeProc{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer conn.Close()
		seq := uint16(0)
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
			}
			seq++
			pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: uint32(seq) * 3750, SSRC: 1234, Marker: true},
				Payload: []byte{0x65, 0x88, 0x84, 0x00, 0x33}}
			b, _ := pkt.Marshal()
			_, _ = conn.Write(b)
		}
	}()
	return p, nil
}

func (f *fakeEncoder) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func TestFFmpegArgsMatchDesign(t *testing.T) {
	got := strings.Join(FFmpegArgs(":0", 1920, 1080, 24, "2500k", "rtp://127.0.0.1:5004"), " ")
	want := "-f x11grab -framerate 24 -video_size 1920x1080 -i :0.0 -c:v libx264 -preset ultrafast -tune zerolatency -pix_fmt yuv420p -b:v 2500k -f rtp rtp://127.0.0.1:5004"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
}

func TestViewerReceivesVideoAndSendsInput(t *testing.T) {
	enc := &fakeEncoder{}
	inputs := make(chan string, 16)
	s, err := New(Options{RTPAddr: "127.0.0.1:0", Encoder: enc, IncludeLoopback: true, Logger: slog.New(slog.DiscardHandler),
		Input: func(b []byte) error { inputs <- string(b); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Browser side.
	se := pion.SettingEngine{}
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]pion.NetworkType{pion.NetworkTypeUDP4})
	me := &pion.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	browser, err := pion.NewAPI(pion.WithMediaEngine(me), pion.WithSettingEngine(se)).NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	if _, err := browser.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	dc, err := browser.CreateDataChannel("input", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotVideo := make(chan string, 1)
	browser.OnTrack(func(tr *pion.TrackRemote, _ *pion.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			gotVideo <- tr.Codec().MimeType
		}
	})

	toBrowser := make(chan Signal, 64)
	peer, err := s.NewPeer("", func(m Signal) { toBrowser <- m })
	if err != nil {
		t.Fatal(err)
	}
	if s.Viewers() != 1 {
		t.Fatalf("viewers %d", s.Viewers())
	}
	browser.OnICECandidate(func(c *pion.ICECandidate) {
		if c == nil {
			return
		}
		init := c.ToJSON()
		b, _ := json.Marshal(Signal{Type: "ice", Candidate: &init})
		_ = peer.HandleSignal(b)
	})
	offer, err := browser.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := browser.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Signal{Type: "offer", SDP: offer.SDP})
	if err := peer.HandleSignal(b); err != nil {
		t.Fatal(err)
	}
	go func() {
		for m := range toBrowser {
			switch m.Type {
			case "answer":
				_ = browser.SetRemoteDescription(pion.SessionDescription{Type: pion.SDPTypeAnswer, SDP: m.SDP})
			case "ice":
				_ = browser.AddICECandidate(*m.Candidate)
			}
		}
	}()

	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	select {
	case <-opened:
	case <-time.After(20 * time.Second):
		t.Fatal("data channel did not open")
	}
	if err := dc.SendText(`{"t":"move","x":1,"y":2}`); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-inputs:
		if in != `{"t":"move","x":1,"y":2}` {
			t.Fatalf("input %q", in)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("input not delivered")
	}
	select {
	case mime := <-gotVideo:
		if mime != pion.MimeTypeH264 {
			t.Fatalf("codec %s", mime)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no video RTP received")
	}
	if !s.Encoding() || enc.count() != 1 {
		t.Fatalf("encoding %v starts %d", s.Encoding(), enc.count())
	}

	// A second viewer shares the encoder; the encoder stops after the last.
	peer2, err := s.NewPeer("203.0.113.7", func(Signal) {})
	if err != nil {
		t.Fatal(err)
	}
	if s.Viewers() != 2 || enc.count() != 1 {
		t.Fatalf("viewers %d starts %d", s.Viewers(), enc.count())
	}
	peer.Close()
	peer2.Close()
	peer2.Close()
	if s.Viewers() != 0 || s.Encoding() {
		t.Fatalf("after close: viewers %d encoding %v", s.Viewers(), s.Encoding())
	}
}
