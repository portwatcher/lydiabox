// Package harness is the unified session manager and the cross-harness
// session bridge (docs/design.md section 4.4). It discovers the sessions of
// the five supported harness CLIs from the host copy of /home/agent, runs
// harness terminals in the guest through a PTYRunner, and hot-switches a
// running task from one harness to another.
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

// Message is one user or assistant turn in a trajectory.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Trajectory limits from docs/design.md section 4.4.2.
const (
	MaxToolActions   = 30
	MaxRecentMessage = 10
)

// CanonicalTrajectory is the harness-neutral summary of a session that the
// bridge hands to the next harness.
type CanonicalTrajectory struct {
	Harness      string `json:"harness"`
	SessionID    string `json:"sessionId"`
	WorkspaceDir string `json:"workspaceDir"`
	Model        string `json:"model,omitempty"`

	OriginalGoal       string    `json:"originalGoal"`
	ModifiedFiles      []string  `json:"modifiedFiles"`
	ToolActionsSummary []string  `json:"toolActionsSummary"`
	RecentMessages     []Message `json:"recentMessages"`
	UnresolvedIssues   []string  `json:"unresolvedIssues"`
	Usage              *Usage    `json:"usage,omitempty"`

	// HandoffPath is the guest path of .agent/HANDOFF.md. The bridge sets it
	// before SynthesizeHandoff so seed sessions can point at it.
	HandoffPath string `json:"handoffPath,omitempty"`
	// HandoffMarkdown is the rendered handoff, set by the bridge.
	HandoffMarkdown string `json:"-"`
}

// HarnessAdapter reads and seeds the on-disk sessions of one harness.
// homeDir is always the host path of the guest /home/agent; workspaceDir is
// the guest working directory.
type HarnessAdapter interface {
	Name() string // "claude-code" | "codex" | "dsh" | "antigravity" | "opencode"
	DiscoverSessions(homeDir string) ([]SessionMeta, error)
	ParseTranscript(homeDir, sessionID string) (*CanonicalTrajectory, error)
	SynthesizeHandoff(homeDir, workspaceDir string, traj *CanonicalTrajectory) (resumeCmd []string, newSessionID string, err error)
}

// LaunchOptions describes a harness start that is not a handoff.
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

// LeaseAcquirer is implemented by wakers that track activity leases. The
// manager holds a "pty" lease for every live harness terminal.
type LeaseAcquirer interface {
	AcquireLease(tenantID, kind string) (release func())
}
