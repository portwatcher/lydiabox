// Package harness is the unified session manager (docs/design.md section
// 4.4). It discovers the sessions of the five supported harness CLIs from
// the host copy of /home/agent and runs harness terminals in the guest
// through a PTYRunner.
package harness

import (
	"context"
	"time"
)

// Harness IDs. Shell is a plain login shell without transcripts.
const (
	ClaudeCode  = "claude-code"
	Codex       = "codex"
	DSH         = "dsh"
	Antigravity = "antigravity"
	OpenCode    = "opencode"
	Shell       = "shell"
)

// Harnesses lists the harnesses that have adapters, in display order.
var Harnesses = []string{ClaudeCode, Codex, DSH, Antigravity, OpenCode}

// Guest paths of the two persistent trees.
const (
	GuestHome      = "/home/agent"
	GuestWorkspace = "/workspace"
)

// Usage is the token accounting of one session.
type Usage struct {
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// IsZero reports whether u carries no data.
func (u *Usage) IsZero() bool {
	return u == nil || (u.InputTokens == 0 && u.OutputTokens == 0 && u.CostUSD == 0)
}

// SessionMeta is one session found on disk by an adapter.
type SessionMeta struct {
	Harness string `json:"harness"`
	// NativeID is the harness's own session ID.
	NativeID string `json:"nativeId"`
	Title    string `json:"title"`
	// WorkspaceDir is the guest working directory of the session.
	WorkspaceDir string    `json:"workspaceDir"`
	Model        string    `json:"model"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Usage        *Usage    `json:"usage,omitempty"`
	// Files are the host paths that hold the transcript (for purge).
	Files []string `json:"-"`
}

// HarnessAdapter reads the on-disk sessions of one harness. homeDir is
// always the host path of the guest /home/agent.
type HarnessAdapter interface {
	Name() string // "claude-code" | "codex" | "dsh" | "antigravity" | "opencode"
	DiscoverSessions(homeDir string) ([]SessionMeta, error)
}

// LaunchOptions describes a harness start.
type LaunchOptions struct {
	WorkspaceDir string
	Model        string
	// ResumeNativeID resumes an existing native session when set.
	ResumeNativeID string
}

// Launcher builds the guest command line of a harness.
type Launcher interface {
	// LaunchCommand returns the command and the native session ID it will
	// use. The ID is empty when the harness picks it at runtime.
	LaunchCommand(opts LaunchOptions) (cmd []string, nativeID string)
}

// Purger deletes the native transcript of one session.
type Purger interface {
	Purge(homeDir, sessionID string) error
}

// Adapter is everything the manager needs from one harness.
type Adapter interface {
	HarnessAdapter
	Launcher
	Purger
}

// PTYSpec describes a PTY to start in the guest. Its fields match the
// guest client's spec so the Phase 6 glue converts it directly.
type PTYSpec struct {
	ID      string
	Command []string
	Dir     string
	Env     map[string]string
	Cols    uint16
	Rows    uint16
}

// PTYInfo describes a PTY that exists in the guest.
type PTYInfo struct {
	ID       string
	Command  []string
	Dir      string
	Running  bool
	ExitCode int
	// ExitedAt is when the process ended (zero while it runs).
	ExitedAt time.Time
}

// PTY is one terminal in the guest. Read returns io.EOF after the process
// exits and the output is drained.
type PTY interface {
	ID() string
	// Replay returns the guest ring buffer captured at attach time.
	Replay() []byte
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows uint16) error
	// Signal sends "SIGINT", "SIGTERM", "SIGHUP", or "SIGQUIT".
	Signal(sig string) error
	// Wait blocks until the process exits and returns its exit code.
	Wait(ctx context.Context) (int, error)
	// Close detaches from the PTY without killing the process.
	Close() error
}

// PTYRunner starts and attaches guest PTYs. Phase 6 adapts the guest client
// (internal/vmm/guestclient) to it.
type PTYRunner interface {
	StartPTY(ctx context.Context, tenantID string, spec PTYSpec) (PTY, error)
	AttachPTY(ctx context.Context, tenantID, id string) (PTY, error)
	ListPTYs(ctx context.Context, tenantID string) ([]PTYInfo, error)
}

// VMWaker boots or resumes the tenant VM. Phase 6 passes the vmm controller.
type VMWaker interface {
	EnsureRunning(ctx context.Context, tenantID string) error
}

// VMStarter is implemented by wakers that tell an explicit start from a
// wake on demand (vmm.Controller): EnsureRunning does not start a VM that
// the user stopped, Start does. A new or resumed session is an explicit
// request of the user, so Create starts the VM with Start.
type VMStarter interface {
	Start(ctx context.Context, tenantID string) error
}

// VMHolder is implemented by wakers that know whether the user stopped the
// VM (vmm.Controller). A terminal that ends while the VM is held ends with
// the CodeVMStopped error instead of an exit frame.
type VMHolder interface {
	Held(tenantID string) bool
}

// CodeVMStopped is the error code of a terminal socket that the server
// refused or ended because the user stopped the VM (api.CodeVMStopped).
// Clients do not reconnect after it.
const CodeVMStopped = "stopped"

// VMPauseReporter is implemented by wakers that know whether the VM
// pauses or is paused (vmm.Controller). A terminal whose stream ends then
// is detached, not exited: the process lives on in the paused guest, and
// the terminal attaches again after a wake. The clients get CodeVMPaused.
type VMPauseReporter interface {
	Paused(tenantID string) bool
}

// CodeVMPaused is the error code of a terminal socket that the server
// ended because the VM was hibernated. Clients reconnect after the VM
// runs again; the reconnect itself wakes the VM.
const CodeVMPaused = "paused"

// LeaseAcquirer is implemented by wakers that track activity leases. The
// manager holds a "pty" lease for every live harness terminal.
type LeaseAcquirer interface {
	AcquireLease(tenantID, kind string) (release func())
}
