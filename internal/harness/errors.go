package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// Error classes. Wrap them with fmt.Errorf("%w: ...") so the API layer can
// map them to HTTP status codes with errors.Is.
var (
	ErrNotFound    = errors.New("not found")
	ErrBadRequest  = errors.New("bad request")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("unavailable")
	// ErrNotInstalled: the program of a harness is not installed in the
	// VM (*NotInstalledError).
	ErrNotInstalled = errors.New("not installed")
)

// NotInstalledError reports a harness whose program the guest did not
// find. Its text names the harness for the person who started it.
type NotInstalledError struct {
	Harness string
	Command string
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("%s is not installed in the VM (the command %q was not found)", HarnessName(e.Harness), e.Command)
}

// Is matches ErrNotInstalled.
func (e *NotInstalledError) Is(target error) bool { return target == ErrNotInstalled }

// harnessNames are the display names of the harnesses (web/src/lib/harness.ts).
var harnessNames = map[string]string{
	ClaudeCode: "Claude Code", Codex: "Codex", DSH: "dsh", Antigravity: "Antigravity", OpenCode: "OpenCode", Shell: "Shell",
}

// HarnessName returns the display name of a harness ID.
func HarnessName(id string) string {
	if n, ok := harnessNames[id]; ok {
		return n
	}
	return id
}

// commandNotFound reports the guest answer to a PTY start whose program is
// not on the PATH (ptymanager: CodeBadRequest "command %q not found").
func commandNotFound(err error) bool {
	var e *rpc.Error
	return errors.As(err, &e) && e.Code == rpc.CodeBadRequest &&
		strings.HasPrefix(e.Message, "command ") && strings.HasSuffix(e.Message, " not found")
}

// ErrNoTranscript is returned by adapters when a session has no transcript.
var ErrNoTranscript = fmt.Errorf("%w: session transcript", ErrNotFound)

// UnavailableRunner is the PTYRunner used until the guest client is wired:
// every call fails with ErrUnavailable (HTTP 503).
type UnavailableRunner struct{ Reason string }

func (u UnavailableRunner) err() error {
	reason := u.Reason
	if reason == "" {
		reason = "the agent VM terminal service is not available in this build"
	}
	return fmt.Errorf("%w: %s", ErrUnavailable, reason)
}

// StartPTY implements PTYRunner.
func (u UnavailableRunner) StartPTY(context.Context, string, PTYSpec) (PTY, error) {
	return nil, u.err()
}

// AttachPTY implements PTYRunner.
func (u UnavailableRunner) AttachPTY(context.Context, string, string) (PTY, error) {
	return nil, u.err()
}

// ListPTYs implements PTYRunner.
func (u UnavailableRunner) ListPTYs(context.Context, string) ([]PTYInfo, error) { return nil, u.err() }

// NoopWaker is a VMWaker for builds without a VM controller.
type NoopWaker struct{}

// EnsureRunning implements VMWaker.
func (NoopWaker) EnsureRunning(context.Context, string) error { return nil }
