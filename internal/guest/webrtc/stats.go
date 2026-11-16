package webrtc

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// DefaultStatsInterval is how often the guest logs the forwarded stream.
const DefaultStatsInterval = 30 * time.Second

// H.264 NAL unit types (RFC 6184).
const (
	nalSPS  = 7
	nalSTAP = 24
	nalFUA  = 28
)

// streamStats counts the RTP that one encoder run forwards and the picture
// loss requests of the viewers. It is safe for concurrent use.
type streamStats struct {
	packets, bytes, keyframes, oversize, pictureLoss atomic.Uint64
	maxPacket                                        atomic.Int64
}

// add counts one RTP packet; it returns true for the first packet and for
// the first packet above limit.
func (st *streamStats) add(pkt []byte, limit int) (first, firstOversize bool) {
	first = st.packets.Add(1) == 1
	st.bytes.Add(uint64(len(pkt)))
	for {
		m := st.maxPacket.Load()
		if int64(len(pkt)) <= m || st.maxPacket.CompareAndSwap(m, int64(len(pkt))) {
			break
		}
	}
	if limit > 0 && len(pkt) > limit {
		firstOversize = st.oversize.Add(1) == 1
	}
	if startsKeyframe(pkt) {
		st.keyframes.Add(1)
	}
	return first, firstOversize
}

// countRTCP counts the picture loss and full intra requests in a viewer's
// RTCP packet.
func (st *streamStats) countRTCP(buf []byte) {
	pkts, err := rtcp.Unmarshal(buf)
	if err != nil {
		return
	}
	for _, p := range pkts {
		switch p.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			st.pictureLoss.Add(1)
		}
	}
}

// statsSnapshot is one reading of the counters.
type statsSnapshot struct {
	packets, bytes, keyframes, oversize, pictureLoss uint64
	maxPacket                                        int64
}

func (st *streamStats) snapshot() statsSnapshot {
	return statsSnapshot{packets: st.packets.Load(), bytes: st.bytes.Load(), keyframes: st.keyframes.Load(),
		oversize: st.oversize.Load(), pictureLoss: st.pictureLoss.Load(), maxPacket: st.maxPacket.Load()}
}

// logAttrs gives the totals and the change since prev.
func (s statsSnapshot) logAttrs(prev statsSnapshot, viewers int) []any {
	return []any{"viewers", viewers, "packets", s.packets - prev.packets, "bytes", s.bytes - prev.bytes,
		"keyframes", s.keyframes - prev.keyframes, "pictureLossRequests", s.pictureLoss - prev.pictureLoss,
		"maxPacket", s.maxPacket, "oversizePackets", s.oversize, "totalPackets", s.packets, "totalBytes", s.bytes}
}

// logStats writes one line every interval until stop is closed.
func (s *Streamer) logStats(st *streamStats, stop <-chan struct{}) {
	if s.opts.StatsInterval < 0 {
		return
	}
	t := time.NewTicker(s.opts.StatsInterval)
	defer t.Stop()
	var prev statsSnapshot
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		cur := st.snapshot()
		level := slog.LevelInfo
		if cur.packets == prev.packets {
			level = slog.LevelWarn
		}
		s.opts.Logger.Log(context.Background(), level, "desktop stream", cur.logAttrs(prev, s.Viewers())...)
		prev = cur
	}
}

// startsKeyframe reports whether an H.264 RTP packet carries a sequence
// parameter set: x264 sends one before each keyframe (IDR), so the count of
// these packets is the count of keyframes.
func startsKeyframe(pkt []byte) bool {
	var h rtp.Header
	n, err := h.Unmarshal(pkt)
	if err != nil || n >= len(pkt) {
		return false
	}
	payload := pkt[n:]
	switch t := payload[0] & 0x1f; t {
	case nalSPS:
		return true
	case nalSTAP:
		for i := 1; i+2 < len(payload); {
			size := int(payload[i])<<8 | int(payload[i+1])
			if payload[i+2]&0x1f == nalSPS {
				return true
			}
			i += 2 + size
		}
	case nalFUA:
		return len(payload) > 1 && payload[1]&0x80 != 0 && payload[1]&0x1f == nalSPS
	}
	return false
}
