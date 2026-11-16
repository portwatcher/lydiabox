// Package webrtc streams the guest desktop to browsers. On the first viewer
// it starts ffmpeg (x11grab -> H.264 -> RTP on 127.0.0.1:5004), copies the
// RTP packets into one shared TrackLocalStaticRTP, and serves it to one
// PeerConnection per viewer. The "input" data channel of each viewer feeds
// the X11 controller, which injects it only in TAKEOVER mode. ffmpeg stops
// when the last viewer leaves.
package webrtc

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/transport/v3"
	pion "github.com/pion/webrtc/v4"
)

// Options configures a Streamer.
type Options struct {
	Display   string // ":0"
	Width     int    // 1920
	Height    int    // 1080
	Framerate int    // 24
	Bitrate   string // "2500k"
	// RTPAddr is where the encoder sends RTP (default 127.0.0.1:5004).
	RTPAddr string
	// PacketSize is the largest RTP packet of the encoder
	// (default DefaultRTPPacketSize).
	PacketSize int
	// KeyframeInterval is the time between two keyframes
	// (default DefaultKeyframeSeconds).
	KeyframeInterval time.Duration
	// StatsInterval is how often the stream counters are logged
	// (default DefaultStatsInterval; negative disables).
	StatsInterval time.Duration
	// UDPPort, when non-zero, carries all WebRTC media on one UDP port so
	// the VMM driver can forward it from the host.
	UDPPort int
	// Encoder starts ffmpeg (default ExecLauncher{Path: "ffmpeg"}).
	Encoder Launcher
	// Input receives every "input" data channel message.
	Input func(data []byte) error
	// IncludeLoopback adds loopback ICE candidates (tests).
	IncludeLoopback bool
	// Net replaces the OS network for ICE (tests: a pion vnet).
	Net transport.Net
	// ICETimeouts overrides the pion ICE timeouts (tests).
	ICETimeouts *ICETimeouts
	Logger      *slog.Logger
}

// ICETimeouts are the pion ICE disconnected, failed, and keepalive timeouts.
type ICETimeouts struct {
	Disconnected, Failed, KeepAlive time.Duration
}

// h264Codec is the codec of the desktop track. libx264 with -preset
// ultrafast makes Constrained Baseline (profile-level-id 42e0xx), and
// ffmpeg's RTP muxer sends FU-A and STAP-A (packetization-mode 1). With
// this fmtp the track takes the payload type of that profile in the
// browser's offer; without a match it takes the first H.264 type.
var h264Codec = pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000,
	SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}

// Streamer owns the shared video track and the encoder lifecycle.
type Streamer struct {
	opts  Options
	track *pion.TrackLocalStaticRTP
	mux   ice.UDPMux
	muxPC net.PacketConn
	stats atomic.Pointer[streamStats] // of the running encoder

	mu      sync.Mutex
	viewers int
	run     *encoderRun
}

type encoderRun struct {
	stop chan struct{}
	done chan struct{}
}

// New returns a streamer. With UDPPort set it binds the media port now.
func New(opts Options) (*Streamer, error) {
	if opts.Display == "" {
		opts.Display = ":0"
	}
	if opts.Width == 0 || opts.Height == 0 {
		opts.Width, opts.Height = 1920, 1080
	}
	if opts.Framerate == 0 {
		opts.Framerate = 24
	}
	if opts.Bitrate == "" {
		opts.Bitrate = "2500k"
	}
	if opts.RTPAddr == "" {
		opts.RTPAddr = "127.0.0.1:5004"
	}
	if opts.PacketSize == 0 {
		opts.PacketSize = DefaultRTPPacketSize
	}
	if opts.KeyframeInterval == 0 {
		opts.KeyframeInterval = DefaultKeyframeSeconds * time.Second
	}
	if opts.StatsInterval == 0 {
		opts.StatsInterval = DefaultStatsInterval
	}
	if opts.Encoder == nil {
		opts.Encoder = ExecLauncher{Path: "ffmpeg"}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	track, err := pion.NewTrackLocalStaticRTP(h264Codec, "video", "lydiabox-desktop")
	if err != nil {
		return nil, err
	}
	s := &Streamer{opts: opts, track: track}
	if opts.UDPPort > 0 {
		pc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: opts.UDPPort})
		if err != nil {
			return nil, fmt.Errorf("webrtc: listen media port %d: %w", opts.UDPPort, err)
		}
		s.muxPC = pc
		s.mux = pion.NewICEUDPMux(nil, pc)
	}
	return s, nil
}

// Close stops the encoder and releases the media port.
func (s *Streamer) Close() error {
	s.mu.Lock()
	run := s.run
	s.run = nil
	s.viewers = 0
	s.mu.Unlock()
	if run != nil {
		close(run.stop)
		<-run.done
	}
	if s.mux != nil {
		return s.mux.Close()
	}
	return nil
}

// Viewers returns the number of connected viewers.
func (s *Streamer) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewers
}

// Encoding reports whether the encoder is running.
func (s *Streamer) Encoding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run != nil
}

func (s *Streamer) acquire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewers++
	if s.run == nil {
		s.run = &encoderRun{stop: make(chan struct{}), done: make(chan struct{})}
		go s.supervise(s.run)
	}
}

func (s *Streamer) release() {
	s.mu.Lock()
	s.viewers--
	var run *encoderRun
	if s.viewers <= 0 {
		s.viewers = 0
		run, s.run = s.run, nil
	}
	s.mu.Unlock()
	if run != nil {
		close(run.stop)
		<-run.done
	}
}

// supervise keeps the encoder running until stop is closed, restarting it
// with a delay when it exits on its own.
func (s *Streamer) supervise(run *encoderRun) {
	defer close(run.done)
	backoff := time.Second
	for {
		err := s.encodeOnce(run.stop)
		select {
		case <-run.stop:
			return
		default:
		}
		s.opts.Logger.Warn("desktop encoder exited; restarting", "err", err, "delay", backoff)
		select {
		case <-run.stop:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Streamer) encodeOnce(stop <-chan struct{}) error {
	addr, err := net.ResolveUDPAddr("udp4", s.opts.RTPAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", addr)
	if err != nil {
		return fmt.Errorf("webrtc: listen RTP %s: %w", s.opts.RTPAddr, err)
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(4 << 20)
	p := EncoderParams{Display: s.opts.Display, Width: s.opts.Width, Height: s.opts.Height, Framerate: s.opts.Framerate,
		Bitrate: s.opts.Bitrate, RTPURL: "rtp://" + conn.LocalAddr().String(), PacketSize: s.opts.PacketSize,
		GOP: max(1, int(s.opts.KeyframeInterval.Seconds()*float64(s.opts.Framerate)+0.5))}
	proc, err := s.opts.Encoder.Start(FFmpegArgs(p))
	if err != nil {
		return err
	}
	started := time.Now()
	s.opts.Logger.Info("desktop encoder started", "rtp", p.RTPURL, "size", fmt.Sprintf("%dx%d", p.Width, p.Height),
		"framerate", p.Framerate, "bitrate", p.Bitrate, "packetSize", p.PacketSize, "keyframeEvery", p.GOP)
	st := &streamStats{}
	s.stats.Store(st)
	statsStop := make(chan struct{})
	go s.logStats(st, statsStop)
	readDone := make(chan error, 1)
	go func() { readDone <- s.copyRTP(conn, st, started) }()
	select {
	case <-stop:
		err = nil
	case <-proc.Done():
		err = fmt.Errorf("encoder exited: %v", exitError(proc))
	case err = <-readDone:
	}
	close(statsStop)
	_ = proc.Stop()
	conn.Close()
	snap := st.snapshot()
	s.opts.Logger.Info("desktop encoder stopped", "after", time.Since(started).Round(time.Second),
		"packets", snap.packets, "bytes", snap.bytes, "keyframes", snap.keyframes, "maxPacket", snap.maxPacket)
	return err
}

// exitError returns the exit status of an encoder process that has one.
func exitError(p Process) error {
	if e, ok := p.(interface{ Err() error }); ok {
		return e.Err()
	}
	return nil
}

func (s *Streamer) copyRTP(conn *net.UDPConn, st *streamStats, started time.Time) error {
	buf := make([]byte, 64<<10)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		first, oversize := st.add(buf[:n], s.opts.PacketSize)
		if first {
			s.opts.Logger.Info("desktop encoder sends RTP", "after", time.Since(started).Round(time.Millisecond))
		}
		if oversize {
			s.opts.Logger.Warn("desktop RTP packet above the packet size; a relayed path can drop it",
				"size", n, "packetSize", s.opts.PacketSize)
		}
		if _, err := s.track.Write(buf[:n]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			s.opts.Logger.Debug("write RTP", "err", err)
		}
	}
}
