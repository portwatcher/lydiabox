package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// ModelBinder binds a harness to a model through its config wirer
// (implemented by *magpie.Bindings).
type ModelBinder interface {
	BindModel(ctx context.Context, tenantID, harness, model string) error
}

// Bridge performs mid-task harness hot-switches (docs/design.md 4.4.2).
type Bridge struct {
	Manager *Manager
	Git     GitInspector
	Binder  ModelBinder
	Logger  *slog.Logger
	Now     func() time.Time
}

// SwitchRequest is the body of POST /api/v1/sessions/switch.
type SwitchRequest struct {
	SourceSessionID string `json:"sourceSessionId"`
	TargetHarness   string `json:"targetHarness"`
	TargetModel     string `json:"targetModel"`
	DryRun          bool   `json:"dryRun"`
}

// SwitchResult is the response of a switch or a dry run.
type SwitchResult struct {
	DryRun            bool          `json:"dryRun"`
	HandoffMarkdown   string        `json:"handoffMarkdown,omitempty"`
	State             *HandoffState `json:"state,omitempty"`
	PreviousSessionID string        `json:"previousSessionId,omitempty"`
	Session           *Session      `json:"session,omitempty"`
	HandoffPath       string        `json:"handoffPath,omitempty"`
}

func (b *Bridge) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *Bridge) log() *slog.Logger {
	if b.Logger != nil {
		return b.Logger
	}
	return slog.Default()
}

// SwitchHarness interrupts the source session, extracts its trajectory and
// the workspace git state, writes .agent/HANDOFF.md, .agent/state.json, and
// the managed blocks, binds the target model, and starts the target harness
// in the same terminal room. With DryRun it only returns the preview.
func (b *Bridge) SwitchHarness(ctx context.Context, tenantID string, req SwitchRequest) (*SwitchResult, error) {
	m := b.Manager
	src, err := m.Get(ctx, tenantID, req.SourceSessionID)
	if err != nil {
		return nil, err
	}
	srcAdapter, ok := m.Adapter(src.Harness)
	if !ok {
		return nil, fmt.Errorf("%w: harness %q has no transcript to hand off", ErrBadRequest, src.Harness)
	}
	target, ok := m.Adapter(req.TargetHarness)
	switch {
	case !ok:
		return nil, fmt.Errorf("%w: unknown target harness %q", ErrBadRequest, req.TargetHarness)
	case req.TargetHarness == src.Harness:
		return nil, fmt.Errorf("%w: the session already runs %s", ErrBadRequest, src.Harness)
	case src.WorkspaceDir == "":
		return nil, fmt.Errorf("%w: the source session has no known workspace directory", ErrBadRequest)
	}
	if req.TargetModel != "" && !validModel(req.TargetModel) {
		return nil, fmt.Errorf("%w: targetModel must be a provider/model ID", ErrBadRequest)
	}
	wsRoot, wsRel, err := hostRoot(m.opts.Dirs, tenantID, src.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	home, err := m.opts.Dirs.HomeDir(tenantID)
	if err != nil {
		return nil, err
	}

	// Step 1: interrupt the source terminal (not for a preview).
	var room *Room
	if !req.DryRun {
		if ls := m.lookupLive(tenantID, src.ID); ls != nil {
			room = ls.room
			m.stop(ctx, ls, true)
		}
	}
	fail := func(err error) (*SwitchResult, error) {
		if room != nil {
			code := "internal"
			if errors.Is(err, ErrUnavailable) {
				code = "unavailable"
			}
			room.fail(code, "hot-switch failed: "+err.Error())
		}
		return nil, err
	}

	// Step 2: canonical trajectory of the source.
	traj, err := srcAdapter.ParseTranscript(home, src.NativeID)
	if errors.Is(err, ErrNotFound) {
		traj = &CanonicalTrajectory{Harness: src.Harness, SessionID: src.NativeID, OriginalGoal: src.Title,
			ModifiedFiles: []string{}, ToolActionsSummary: []string{}, RecentMessages: []Message{}, UnresolvedIssues: []string{}}
	} else if err != nil {
		return fail(fmt.Errorf("parse %s transcript: %w", src.ID, err))
	}
	traj.WorkspaceDir = src.WorkspaceDir

	// Step 3: git state of the workspace from the host filesystem.
	var gitStatus, gitDiff string
	if b.Git != nil {
		wsHost, rerr := safefile.Resolve(wsRoot, wsRel)
		if rerr == nil {
			gitStatus, gitDiff, err = b.Git.Inspect(ctx, wsHost)
		}
		if rerr != nil || err != nil {
			b.log().Warn("workspace git inspection skipped", "workspace", src.WorkspaceDir, "err", errors.Join(rerr, err))
		}
	}
	at := b.now()
	state := buildState(traj, gitStatus, gitDiff)
	markdown := RenderHandoff(state, src.ID, req.TargetHarness, at)
	if req.DryRun {
		return &SwitchResult{DryRun: true, HandoffMarkdown: markdown, State: &state}, nil
	}

	// Step 4: .agent/HANDOFF.md, .agent/state.json, managed blocks.
	sf := stateFile{HandoffState: state, SourceSessionID: src.ID, SourceHarness: src.Harness, TargetHarness: req.TargetHarness,
		TargetModel: req.TargetModel, WorkspaceDir: src.WorkspaceDir, GeneratedAt: at}
	if err := writeHandoff(wsRoot, wsRel, markdown, sf); err != nil {
		return fail(err)
	}
	handoffPath := path.Join(src.WorkspaceDir, ".agent", "HANDOFF.md")

	// Step 5: bind the target model.
	if req.TargetModel != "" && b.Binder != nil {
		if err := b.Binder.BindModel(ctx, tenantID, req.TargetHarness, req.TargetModel); err != nil {
			return fail(fmt.Errorf("bind %s to %s: %w", req.TargetHarness, req.TargetModel, err))
		}
	}

	// Step 6: native seed session and the target terminal in the same room.
	traj.HandoffPath, traj.HandoffMarkdown, traj.Model = handoffPath, markdown, req.TargetModel
	cmd, nativeID, err := target.SynthesizeHandoff(home, src.WorkspaceDir, traj)
	if err != nil {
		return fail(fmt.Errorf("synthesize %s session: %w", req.TargetHarness, err))
	}
	ls := &liveSession{tenantID: tenantID, harness: req.TargetHarness, nativeID: nativeID, workspace: src.WorkspaceDir,
		model: req.TargetModel, title: src.Title, provisional: nativeID == ""}
	if ls.provisional {
		ls.nativeID = randomID("new-")
	}
	toID := SessionID(ls.harness, ls.nativeID)
	ctl := CtlSwitched{Type: "switched", FromSessionID: src.ID, ToSessionID: toID, HandoffPath: handoffPath}
	if err := m.start(ctx, ls, cmd, 0, 0, room, &ctl); err != nil {
		return fail(err)
	}
	b.log().Info("harness hot-switch complete", "from", src.ID, "to", toID, "handoff", handoffPath)
	s := m.sessionOf(ls)
	return &SwitchResult{DryRun: false, PreviousSessionID: src.ID, Session: &s, HandoffPath: handoffPath}, nil
}

func validModel(m string) bool {
	prov, rest, ok := strings.Cut(m, "/")
	return ok && prov != "" && rest != "" && len(m) <= 256 && !strings.ContainsAny(m, " \t\n\x00")
}

// writeHandoff writes the handoff files into the workspace.
func writeHandoff(root, rel, markdown string, sf stateFile) error {
	if err := safefile.WriteFile(root, joinRel(rel, ".agent/HANDOFF.md"), []byte(markdown), 0o644); err != nil {
		return fmt.Errorf("write HANDOFF.md: %w", err)
	}
	js, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}
	if err := safefile.WriteFile(root, joinRel(rel, ".agent/state.json"), append(js, '\n'), 0o644); err != nil {
		return fmt.Errorf("write state.json: %w", err)
	}
	for _, name := range InstructionFiles {
		p := joinRel(rel, name)
		old, err := safefile.ReadFile(root, p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("read %s: %w", name, err)
		}
		updated := UpsertManagedBlock(string(old))
		if updated == string(old) {
			continue
		}
		if err := safefile.WriteFile(root, p, []byte(updated), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
