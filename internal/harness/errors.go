package harness

import (
	"context"
	"errors"
	"fmt"
)

// Error classes. Wrap them with fmt.Errorf("%w: ...") so the API layer can
// map them to HTTP status codes with errors.Is.
var (
	ErrNotFound    = errors.New("not found")
	ErrBadRequest  = errors.New("bad request")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("unavailable")
)

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
