package webrtc

import (
	"bytes"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
)

// Sizes of the fake H.264 frames: a keyframe of a 1920x1080 desktop was
// 44 to 57 kB on an InstaBox machine, a frame of a still desktop 250 to
// 500 bytes.
const (
	fakeIDRSize = 40000
	fakePSize   = 300
)

// h264Encoder imitates ffmpeg's RTP output for the arguments it gets:
// SPS, PPS, and an IDR frame every -g frames (x264's default is 250), small
// P frames in between, -framerate frames a second, and RTP packets of at
// most pkt_size bytes (ffmpeg's default is 1472).
type h264Encoder struct {
	mu     sync.Mutex
	starts [][]string
}

func argValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func intOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func (e *h264Encoder) Start(args []string) (Process, error) {
	e.mu.Lock()
	e.starts = append(e.starts, args)
	e.mu.Unlock()
	fps := intOr(argValue(args, "-framerate"), 24)
	gop := intOr(argValue(args, "-g"), 250)
	pktSize := 1472
	if u, err := url.Parse(args[len(args)-1]); err == nil {
		pktSize = intOr(u.Query().Get("pkt_size"), pktSize)
	}
	conn, err := net.Dial("udp", rtpTarget(args))
	if err != nil {
		return nil, err
	}
	p := &fakeProc{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		defer conn.Close()
		pay := &codecs.H264Payloader{}
		seq := uint16(0)
		t := time.NewTicker(time.Second / time.Duration(fps))
		defer t.Stop()
		for frame := 0; ; frame++ {
			select {
			case <-p.stop:
				return
			case <-t.C:
			}
			var au []byte
			if frame%gop == 0 {
				au = append(au, annexB(0x67, 9)...)
				au = append(au, annexB(0x68, 3)...)
				au = append(au, annexB(0x65, fakeIDRSize)...)
			} else {
				au = annexB(0x41, fakePSize)
			}
			payloads := pay.Payload(uint16(pktSize-12), au)
			for i, pl := range payloads {
				seq++
				pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq,
					Timestamp: uint32(frame * 90000 / fps), SSRC: 1234, Marker: i == len(payloads)-1}, Payload: pl}
				b, _ := pkt.Marshal()
				_, _ = conn.Write(b)
			}
		}
	}()
	return p, nil
}

// annexB returns one NAL unit with a start code: the header byte, then
// size bytes without zeros (so no start code appears inside).
func annexB(header byte, size int) []byte {
	b := make([]byte, 0, size+5)
	b = append(b, 0, 0, 0, 1, header)
	for i := range size {
		b = append(b, byte(i%251)+1)
	}
	return b
}

// completeIDR reports whether an Annex B access unit has a whole fake IDR.
func completeIDR(au []byte) bool {
	for _, nal := range bytes.Split(au, []byte{0, 0, 0, 1}) {
		if len(nal) > 0 && nal[0]&0x1f == 5 && len(nal) == fakeIDRSize+1 {
			return true
		}
	}
	return false
}
