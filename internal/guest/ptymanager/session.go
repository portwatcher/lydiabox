//go:build unix

package ptymanager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// subscriberBuffer is the number of output chunks queued per attached
// client. A client that falls further behind is detached so it cannot stall
// the PTY or the other clients in the room.
const subscriberBuffer = 1024

// ErrExited is returned when writing to or resizing an exited PTY.
var ErrExited = errors.New("ptymanager: process has exited")

// Signals accepted by Signal (docs/api.md section 8).
var signals = map[string]syscall.Signal{
	"SIGINT":  syscall.SIGINT,
	"SIGTERM": syscall.SIGTERM,
	"SIGHUP":  syscall.SIGHUP,
	"SIGQUIT": syscall.SIGQUIT,
	"SIGKILL": syscall.SIGKILL,
}

// ParseSignal maps a signal name (with or without the SIG prefix).
func ParseSignal(name string) (syscall.Signal, error) {
	n := strings.ToUpper(strings.TrimSpace(name))
	if !strings.HasPrefix(n, "SIG") {
		n = "SIG" + n
	}
	sig, ok := signals[n]
	if !ok {
		return 0, rpc.Errorf(rpc.CodeBadRequest, "unsupported signal %q", name)
	}
	return sig, nil
}

// Session is one PTY process with its replay buffer and attached clients
// (the terminal room).
type Session struct {
	id        string
	command   []string
	dir       string
	startedAt time.Time

	ptmx *os.File
	cmd  *exec.Cmd
	done chan struct{}

	mu       sync.Mutex
	ring     *Ring
	subs     map[*Subscriber]struct{}
	cols     uint16
	rows     uint16
	exited   bool
	exitCode int
	signal   string
	exitedAt time.Time
}

// Subscriber receives the live output of a session.
type Subscriber struct {
	ch      chan []byte
	closed  bool
	dropped bool
}

// C delivers output chunks. It is closed when the session exits (after all
// output was delivered), on Detach, or when the subscriber fell behind.
func (s *Subscriber) C() <-chan []byte { return s.ch }

// ID returns the session ID.
func (s *Session) ID() string { return s.id }

// Done is closed when the process has exited and its output is drained.
func (s *Session) Done() <-chan struct{} { return s.done }

// Exit returns the exit code and the terminating signal name (if any).
func (s *Session) Exit() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode, s.signal
}

// Size returns the terminal size.
func (s *Session) Size() (uint16, uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Info describes the session.
func (s *Session) Info() rpc.PTYInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return rpc.PTYInfo{
		ID: s.id, Command: append([]string(nil), s.command...), Dir: s.dir, Running: !s.exited,
		ExitCode: s.exitCode, Signal: s.signal, Cols: s.cols, Rows: s.rows, Attached: len(s.subs),
		StartedAt: s.startedAt, ExitedAt: s.exitedAt,
	}
}

// Attach registers a subscriber and returns the replay snapshot taken
// atomically with the registration, so no output is lost or duplicated.
func (s *Session) Attach() (*Subscriber, []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub := &Subscriber{ch: make(chan []byte, subscriberBuffer)}
	replay := s.ring.Bytes()
	if s.exited {
		sub.closed = true
		close(sub.ch)
		return sub, replay
	}
	s.subs[sub] = struct{}{}
	return sub, replay
}

// Detach removes a subscriber and closes its channel.
func (s *Session) Detach(sub *Subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeSub(sub)
}

// Dropped reports whether the subscriber was detached for falling behind.
func (s *Session) Dropped(sub *Subscriber) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sub.dropped
}

func (s *Session) closeSub(sub *Subscriber) {
	delete(s.subs, sub)
	if !sub.closed {
		sub.closed = true
		close(sub.ch)
	}
}

// Write sends input to the PTY.
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	if exited {
		return 0, ErrExited
	}
	return s.ptmx.Write(p)
}

// Resize changes the terminal size.
func (s *Session) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return rpc.Errorf(rpc.CodeBadRequest, "cols and rows must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return ErrExited
	}
	if err := setWinsize(s.ptmx, cols, rows); err != nil {
		return fmt.Errorf("ptymanager: resize: %w", err)
	}
	s.cols, s.rows = cols, rows
	return nil
}

// Signal delivers a signal to the terminal's foreground process group, as a
// keyboard interrupt would, falling back to the session's process group.
func (s *Session) Signal(name string) error {
	sig, err := ParseSignal(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	exited := s.exited
	s.mu.Unlock()
	if exited {
		return ErrExited
	}
	return s.signalGroup(sig)
}

func (s *Session) signalGroup(sig syscall.Signal) error {
	if pgrp, err := foregroundGroup(s.ptmx); err == nil && pgrp > 0 {
		if err := syscall.Kill(-pgrp, sig); err == nil {
			return nil
		}
	}
	pid := s.cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		if err := s.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return fmt.Errorf("ptymanager: signal %s: %w", sig, err)
		}
	}
	return nil
}

// readLoop copies PTY output into the ring and to every subscriber.
func (s *Session) readLoop(readerDone chan<- struct{}) {
	defer close(readerDone)
	buf := make([]byte, 32<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.publish(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) publish(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring.Write(p)
	chunk := append([]byte(nil), p...)
	for sub := range s.subs {
		select {
		case sub.ch <- chunk:
		default:
			sub.dropped = true
			s.closeSub(sub)
		}
	}
}

// waitLoop reaps the process, drains the output, and publishes the exit.
func (s *Session) waitLoop(readerDone <-chan struct{}, onExit func(*Session)) {
	err := s.cmd.Wait()
	code, sigName := 0, ""
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
		if ws, ok := s.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			sigName = unix.SignalName(ws.Signal())
			code = 128 + int(ws.Signal())
		}
	} else if err != nil {
		code = -1
	}
	// Background jobs can keep the slave side open; give the reader a short
	// grace period to drain, then close the master to unblock it.
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
	}
	s.ptmx.Close()
	<-readerDone

	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	s.signal = sigName
	s.exitedAt = time.Now().UTC()
	for sub := range s.subs {
		s.closeSub(sub)
	}
	s.mu.Unlock()
	close(s.done)
	if onExit != nil {
		onExit(s)
	}
}

// setWinsize and foregroundGroup use SyscallConn so the descriptor stays in
// non-blocking mode (os.File.Fd would switch it to blocking, and Close could
// then no longer interrupt a pending Read).
func setWinsize(f *os.File, cols, rows uint16) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) {
		ierr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	}); err != nil {
		return err
	}
	return ierr
}

func foregroundGroup(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		pgrp int
		ierr error
	)
	if err := rc.Control(func(fd uintptr) { pgrp, ierr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) }); err != nil {
		return 0, err
	}
	return pgrp, ierr
}
