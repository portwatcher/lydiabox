package webrtc

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtp/codecs"
	"github.com/pion/transport/v3/vnet"
	pion "github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/samplebuilder"
)

// testPathMTU is the largest IP packet that reached the TURN server from
// the first tester's network (STUN requests of growing size, 2026-11-16).
// From an InstaBox machine 1500 bytes passed, but no fragmented datagram.
const testPathMTU = 1452

// dropAbovePathMTU makes the virtual internet drop each UDP datagram whose
// IP packet (payload + 8 UDP + 20 IPv4) is larger than mtu, as a path
// drops IP fragments. It returns the drop counter.
func dropAbovePathMTU(w *natWorld, mtu int) *atomic.Int64 {
	dropped := &atomic.Int64{}
	w.wan.AddChunkFilter(func(c vnet.Chunk) bool {
		if c.Network() == "udp" && len(c.UserData())+28 > mtu {
			dropped.Add(1)
			return false
		}
		return true
	})
	return dropped
}

// Both peers relay-only through TURN over UDP, on a path that drops
// packets above testPathMTU. The browser side assembles H.264 frames with
// pion's depacketizer. With ffmpeg's defaults (1472-byte packets, a
// keyframe every 250 frames) no keyframe arrives in the test time, and with
// 1472-byte packets no keyframe arrives whatever the keyframe interval: the
// path drops each full-size packet, as in the first tenant test (RTP
// arrives, about 10 % lost, no frame decoded). With the guest defaults,
// keyframes arrive every DefaultKeyframeSeconds.
func TestRelayedKeyframeFitsPathMTU(t *testing.T) {
	if testing.Short() {
		t.Skip("streams for several seconds")
	}
	cases := []struct {
		name       string
		packetSize int
		keyframe   time.Duration
		want       bool
	}{
		{"ffmpeg defaults, as before the fix", 1472, 250 * time.Second / 24, false},
		{"full-size packets, short keyframe interval", 1472, DefaultKeyframeSeconds * time.Second, false},
		{"guest defaults", 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newNATWorld(t)
			dropped := dropAbovePathMTU(w, testPathMTU)
			srv := turnServer(t, "turn:"+wanTURNIP+":3478?transport=udp")
			keyframes := make(chan struct{}, 16)
			codec := make(chan pion.RTPCodecParameters, 1)
			r := newRig(t, rigConfig{guestNet: w.guest, browserNet: w.brows,
				guest:   PeerConfig{ICEServers: []pion.ICEServer{srv}, RelayOnly: true},
				browser: pion.Configuration{ICEServers: []pion.ICEServer{srv}, ICETransportPolicy: pion.ICETransportPolicyRelay},
				encoder: &h264Encoder{},
				stream: func(o *Options) {
					o.PacketSize, o.KeyframeInterval = c.packetSize, c.keyframe
				},
				onTrack: func(tr *pion.TrackRemote) {
					codec <- tr.Codec()
					sb := samplebuilder.New(512, &codecs.H264Packet{}, 90000)
					for {
						pkt, _, err := tr.ReadRTP()
						if err != nil {
							return
						}
						sb.Push(pkt)
						for s := sb.Pop(); s != nil; s = sb.Pop() {
							if completeIDR(s.Data) {
								select {
								case keyframes <- struct{}{}:
								default:
								}
							}
						}
					}
				}})
			select {
			case <-r.opened:
			case <-time.After(30 * time.Second):
				t.Fatalf("not connected (guest %s, browser %s)", r.peer.ConnectionState(), r.browser.ConnectionState())
			}
			if g, b := r.pairs(t); g != "relay/relay" || b != "relay/relay" {
				t.Fatalf("selected pairs: guest %s, browser %s", g, b)
			}
			got := 0
			deadline := time.After(3*DefaultKeyframeSeconds*time.Second + time.Second)
		wait:
			for got < 2 {
				select {
				case <-keyframes:
					got++
				case <-deadline:
					break wait
				}
			}
			select {
			case cp := <-codec:
				if f := cp.SDPFmtpLine; !strings.Contains(f, "packetization-mode=1") || !strings.Contains(f, "profile-level-id=42e01f") {
					t.Fatalf("negotiated H.264 fmtp %q, want packetization-mode=1 and Constrained Baseline", f)
				}
			default:
				t.Fatal("no video track")
			}
			st := r.s.stats.Load().snapshot()
			t.Logf("keyframes assembled %d; guest sent %d packets, max %d bytes; path dropped %d datagrams",
				got, st.packets, st.maxPacket, dropped.Load())
			if c.want && got < 2 {
				t.Fatalf("assembled %d keyframes, want 2", got)
			}
			if !c.want && got > 0 {
				t.Fatalf("assembled %d keyframes through a %d-byte path, want none", got, testPathMTU)
			}
			if c.keyframe == DefaultKeyframeSeconds*time.Second && !c.want && dropped.Load() == 0 {
				t.Fatal("the path dropped nothing")
			}
		})
	}
}
