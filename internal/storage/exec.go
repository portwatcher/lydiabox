package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Command describes one external process invocation. Env entries are added to
// the parent environment.
type Command struct {
	Path   string
	Args   []string
	Env    []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// String renders the command for logs with secret flag values redacted.
func (c Command) String() string {
	parts := []string{c.Path}
	for _, a := range c.Args {
		parts = append(parts, redactArg(a))
	}
	return strings.Join(parts, " ")
}

func redactArg(a string) string {
	for _, p := range []string{"--secret-key=", "--access-key=", "--password="} {
		if strings.HasPrefix(a, p) {
			return p + "REDACTED"
		}
	}
	return a
}

// Process is a started child process.
type Process interface {
	Pid() int
	Signal(sig os.Signal) error
	Kill() error
	// Wait blocks until the process exits. It may be called by one goroutine only.
	Wait() error
}

// Runner abstracts process execution so the storage supervisor can be tested
// without real juicefs or mkfs.ext4 binaries.
type Runner interface {
	// Run executes cmd to completion. A non-zero exit returns an error that
	// includes the tail of stderr.
	Run(ctx context.Context, cmd Command) error
	// Start launches a long-running process that the caller supervises.
	Start(cmd Command) (Process, error)
}

// OSRunner runs real processes.
type OSRunner struct{}

// Run implements Runner.
func (OSRunner) Run(ctx context.Context, c Command) error {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	configure(cmd, c)
	tail := &tailBuffer{max: 4096}
	if c.Stderr != nil {
		cmd.Stderr = io.MultiWriter(c.Stderr, tail)
	} else {
		cmd.Stderr = tail
	}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(tail.String()); msg != "" {
			return fmt.Errorf("%s: %w: %s", c.String(), err, msg)
		}
		return fmt.Errorf("%s: %w", c.String(), err)
	}
	return nil
}

// Start implements Runner. The child gets its own process group so a terminal
// Ctrl-C reaches only lydiabox, which then shuts children down in order.
func (OSRunner) Start(c Command) (Process, error) {
	cmd := exec.Command(c.Path, c.Args...)
	configure(cmd, c)
	if c.Stderr != nil {
		cmd.Stderr = c.Stderr
	}
	cmd.SysProcAttr = childProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", c.String(), err)
	}
	return &osProcess{cmd: cmd}, nil
}

func configure(cmd *exec.Cmd, c Command) {
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.Stdout = c.Stdout
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
}

type osProcess struct{ cmd *exec.Cmd }

func (p *osProcess) Pid() int                   { return p.cmd.Process.Pid }
func (p *osProcess) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }
func (p *osProcess) Kill() error                { return p.cmd.Process.Kill() }
func (p *osProcess) Wait() error                { return p.cmd.Wait() }

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}
