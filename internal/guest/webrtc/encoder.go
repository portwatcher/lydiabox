package webrtc

import (
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// DefaultRTPPacketSize is the largest RTP packet (header and payload) that
// ffmpeg sends. ffmpeg's own default is 1472 bytes, and a relayed packet of
// that size is 1472 + 16 (SRTP tag, AES-GCM; 10 for HMAC-SHA1-80) + 4 (TURN
// ChannelData) + 8 (UDP) + 20 (IPv4) = 1520 bytes: above a 1500-byte MTU,
// so it is sent as IP fragments, and many paths drop them (an InstaBox
// machine and the first tester's network did: every keyframe was lost).
// With 1100 bytes the worst case is 1100 + 16 + 40 (TURN Send indication)
// + 8 + 40 (IPv6) = 1204 bytes, below the 1280-byte IPv6 minimum MTU.
const DefaultRTPPacketSize = 1100

// DefaultKeyframeSeconds is the keyframe (IDR) interval. A viewer can
// decode only from a keyframe, and the guest cannot ask ffmpeg for one on
// a picture loss request, so a new viewer or a viewer that lost a packet
// waits at most this long for a picture.
const DefaultKeyframeSeconds = 2

// EncoderParams are the ffmpeg settings of the stream.
type EncoderParams struct {
	Display    string
	Width      int
	Height     int
	Framerate  int
	Bitrate    string
	RTPURL     string // rtp://host:port
	PacketSize int    // largest RTP packet in bytes
	GOP        int    // frames from one keyframe to the next
}

// FFmpegArgs returns the ffmpeg arguments of docs/design.md section 4.5.3:
// an x11grab capture encoded to H.264 and sent as RTP to p.RTPURL, with
// RTP packets of at most p.PacketSize bytes and a keyframe every p.GOP
// frames.
func FFmpegArgs(p EncoderParams) []string {
	return []string{
		"-hide_banner", "-nostats",
		"-f", "x11grab", "-framerate", fmt.Sprint(p.Framerate), "-video_size", fmt.Sprintf("%dx%d", p.Width, p.Height),
		"-i", p.Display + ".0",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p", "-b:v", p.Bitrate,
		"-g", fmt.Sprint(p.GOP),
		"-f", "rtp", fmt.Sprintf("%s?pkt_size=%d", p.RTPURL, p.PacketSize),
	}
}

// Process is a running encoder.
type Process interface {
	// Stop terminates the process and waits for it.
	Stop() error
	// Done is closed when the process exits.
	Done() <-chan struct{}
}

// Launcher starts the encoder; tests substitute an RTP generator.
type Launcher interface {
	Start(args []string) (Process, error)
}

// ExecLauncher runs a real binary (ffmpeg).
type ExecLauncher struct {
	Path   string
	Stderr io.Writer
}

// Start implements Launcher.
func (l ExecLauncher) Start(args []string) (Process, error) {
	cmd := exec.Command(l.Path, args...)
	cmd.Stderr = l.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("webrtc: start %s: %w", l.Path, err)
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *execProcess) Done() <-chan struct{} { return p.done }

// Err returns the exit error after Done is closed.
func (p *execProcess) Err() error { return p.err }

func (p *execProcess) Stop() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
	return nil
}
