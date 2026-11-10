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
	"time"

	"github.com/pion/ice/v4"
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
	// UDPPort, when non-zero, carries all WebRTC media on one UDP port so
	// the VMM driver can forward it from the host.
	UDPPort int
	// Encoder starts ffmpeg (default ExecLauncher{Path: "ffmpeg"}).
	Encoder Launcher
	// Input receives every "input" data channel message.
	Input func(data []byte) error
	// IncludeLoopback adds loopback ICE candidates (tests).
	IncludeLoopback bool
	Logger          *slog.Logger
}

// Streamer owns the shared video track and the encoder lifecycle.
type Streamer struct {
	opts  Options
	track *pion.TrackLocalStaticRTP
	mux   ice.UDPMux
	muxPC net.PacketConn

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
	if opts.Encoder == nil {
		opts.Encoder = ExecLauncher{Path: "ffmpeg"}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	track, err := pion.NewTrackLocalStaticRTP(pion.RTPCodecCapability{MimeType: pion.MimeTypeH264, ClockRate: 90000}, "video", "lydiabox-desktop")
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
	rtpURL := "rtp://" + conn.LocalAddr().String()
	proc, err := s.opts.Encoder.Start(FFmpegArgs(s.opts.Display, s.opts.Width, s.opts.Height, s.opts.Framerate, s.opts.Bitrate, rtpURL))
	if err != nil {
		return err
	}
	s.opts.Logger.Info("desktop encoder started", "rtp", rtpURL)
	readDone := make(chan error, 1)
	go func() { readDone <- s.copyRTP(conn) }()
	select {
	case <-stop:
		err = nil
	case <-proc.Done():
		err = errors.New("encoder exited")
	case err = <-readDone:
	}
	_ = proc.Stop()
	conn.Close()
	s.opts.Logger.Info("desktop encoder stopped")
	return err
}

func (s *Streamer) copyRTP(conn *net.UDPConn) error {
	buf := make([]byte, 1600)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if _, err := s.track.Write(buf[:n]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			s.opts.Logger.Debug("write RTP", "err", err)
		}
	}
}
