package webrtc

import (
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// FFmpegArgs returns the ffmpeg arguments of docs/design.md section 4.5.3:
// an x11grab capture encoded to H.264 and sent as RTP to rtpURL.
func FFmpegArgs(display string, width, height, framerate int, bitrate, rtpURL string) []string {
	return []string{
		"-f", "x11grab", "-framerate", fmt.Sprint(framerate), "-video_size", fmt.Sprintf("%dx%d", width, height),
		"-i", display + ".0",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-pix_fmt", "yuv420p", "-b:v", bitrate,
		"-f", "rtp", rtpURL,
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
