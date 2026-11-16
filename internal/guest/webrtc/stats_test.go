package webrtc

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func rtpWith(payload ...byte) []byte {
	b, _ := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96}, Payload: payload}).Marshal()
	return b
}

func TestStartsKeyframe(t *testing.T) {
	cases := map[string]struct {
		pkt  []byte
		want bool
	}{
		"single SPS":          {rtpWith(0x67, 1, 2), true},
		"STAP-A SPS and PPS":  {rtpWith(0x18, 0, 2, 0x67, 1, 0, 2, 0x68, 1), true},
		"STAP-A P slices":     {rtpWith(0x18, 0, 2, 0x41, 1, 0, 2, 0x41, 1), false},
		"FU-A IDR start":      {rtpWith(0x7c, 0x85, 1), false},
		"single P slice":      {rtpWith(0x41, 1), false},
		"header only":         {rtpWith(), false},
		"truncated STAP-A":    {rtpWith(0x18, 0, 9, 0x41), false},
		"not an RTP datagram": {[]byte{1, 2}, false},
	}
	for name, c := range cases {
		if got := startsKeyframe(c.pkt); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

func TestStreamStatsCountAndLog(t *testing.T) {
	st := &streamStats{}
	if first, over := st.add(rtpWith(0x18, 0, 1, 0x67, 0, 1, 0x68), 1100); !first || over {
		t.Fatalf("first packet: first %v oversize %v", first, over)
	}
	if first, over := st.add(make([]byte, 1472), 1100); first || !over {
		t.Fatalf("large packet: first %v oversize %v", first, over)
	}
	if _, over := st.add(make([]byte, 1300), 1100); over {
		t.Fatal("second large packet reported again")
	}
	pli, _ := rtcp.Marshal([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 1}, &rtcp.ReceiverReport{}})
	st.countRTCP(pli)
	s := st.snapshot()
	if s.packets != 3 || s.keyframes != 1 || s.oversize != 2 || s.maxPacket != 1472 || s.pictureLoss != 1 {
		t.Fatalf("snapshot %+v", s)
	}

	var out bytes.Buffer
	str := &Streamer{opts: Options{StatsInterval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(&out, nil))}}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { str.logStats(st, stop); close(done) }()
	time.Sleep(70 * time.Millisecond)
	close(stop)
	<-done
	log := out.String()
	if !strings.Contains(log, `level=INFO msg="desktop stream" viewers=0 packets=3`) ||
		!strings.Contains(log, "keyframes=1 pictureLossRequests=1 maxPacket=1472") ||
		!strings.Contains(log, `level=WARN msg="desktop stream" viewers=0 packets=0`) {
		t.Fatalf("log:\n%s", log)
	}
}
