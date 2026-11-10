package adapters

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// OpenCode reads sst/opencode sessions from its SQLite database or legacy
// JSON storage (see opencode_store.go).
type OpenCode struct{}

// Name implements harness.HarnessAdapter.
func (OpenCode) Name() string { return harness.OpenCode }

func ocMeta(home string, s ocSession, msgs []ocMessage) (harness.SessionMeta, *builder, int) {
	m := harness.SessionMeta{Harness: harness.OpenCode, NativeID: s.ID, Title: s.Title, WorkspaceDir: s.Directory,
		CreatedAt: millis(s.Created), UpdatedAt: millis(s.Updated)}
	if s.FromDB {
		m.Files = []string{hostPath(home, ocDB)}
	}
	for _, rel := range s.Rel {
		m.Files = append(m.Files, hostPath(home, rel))
	}
	b := newBuilder(s.Directory)
	usage := harness.Usage{}
	n := 0
	for _, msg := range msgs {
		if t := millis(msg.Time.Created); t.After(m.UpdatedAt) {
			m.UpdatedAt = t
		}
		if msg.Role == "assistant" {
			if msg.ModelID != "" {
				m.Model = strings.Trim(msg.ProviderID+"/"+msg.ModelID, "/")
			}
			usage.CostUSD += msg.Cost
			if t := msg.Tokens; t != nil {
				usage.InputTokens += t.Input + t.Cache.Read + t.Cache.Write
				usage.OutputTokens += t.Output + t.Reasoning
			}
		}
		for _, p := range msg.Parts {
			switch p.Type {
			case "text":
				if p.Synthetic {
					continue
				}
				if msg.Role == "user" {
					b.user(p.Text)
				} else {
					b.assistant(p.Text)
				}
				n++
			case "tool":
				b.tool(p.CallID, p.Tool, jsonArgs(p.State.Input))
				switch p.State.Status {
				case "error":
					b.result(p.CallID, true, p.State.Error)
				case "completed":
					b.result(p.CallID, false, p.State.Output)
				}
			case "patch":
				for _, f := range p.Files {
					b.addModified(relPath(f, s.Directory))
				}
			}
		}
	}
	if m.Title == "" || strings.HasPrefix(m.Title, "New session - ") {
		m.Title = titleFrom(b.goal)
	}
	if !usage.IsZero() {
		m.Usage = &usage
	}
	return m, b, n
}

// DiscoverSessions implements harness.HarnessAdapter. Child (subagent) and
// archived sessions are skipped.
func (o OpenCode) DiscoverSessions(home string) ([]harness.SessionMeta, error) {
	all, db, err := ocAll(home)
	if err != nil {
		return nil, err
	}
	if db != nil {
		defer db.Close()
	}
	out := []harness.SessionMeta{}
	for _, s := range all {
		if s.ParentID != "" || s.Archived {
			continue
		}
		msgs, err := ocMessagesOf(home, db, s)
		if err != nil {
			continue
		}
		m, _, n := ocMeta(home, s, msgs)
		if n == 0 {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (o OpenCode) load(home, id string) (ocSession, []ocMessage, error) {
	if err := checkID(id); err != nil {
		return ocSession{}, nil, err
	}
	all, db, err := ocAll(home)
	if err != nil {
		return ocSession{}, nil, err
	}
	if db != nil {
		defer db.Close()
	}
	for _, s := range all {
		if s.ID == id {
			msgs, err := ocMessagesOf(home, db, s)
			return s, msgs, err
		}
	}
	return ocSession{}, nil, fmt.Errorf("%w: opencode session %s", harness.ErrNotFound, id)
}

// ParseTranscript implements harness.HarnessAdapter.
func (o OpenCode) ParseTranscript(home, id string) (*harness.CanonicalTrajectory, error) {
	s, msgs, err := o.load(home, id)
	if err != nil {
		return nil, err
	}
	m, b, n := ocMeta(home, s, msgs)
	if n == 0 {
		return nil, harness.ErrNoTranscript
	}
	t := b.finish(o.Name(), id, m.Model)
	t.Usage = m.Usage
	return t, nil
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// ocID makes an opencode-style ascending ID ("ses_", "msg_", "prt_" + 12
// hex digits of time + 14 random base62 characters).
func ocID(prefix string, t time.Time, seq int64) string {
	var b strings.Builder
	b.WriteString(prefix + "_")
	fmt.Fprintf(&b, "%012x", t.UnixMilli()*0x1000+seq)
	for i := 0; i < 14; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(base62))))
		if err != nil {
			panic(err)
		}
		b.WriteByte(base62[n.Int64()])
	}
	return b.String()
}

// ocImportDir holds seed files for `opencode import` below /home/agent.
const ocImportDir = ".local/share/lydiabox/handoff"

// SynthesizeHandoff writes a session in `opencode export` format and
// starts opencode with `opencode import <file>` followed by the TUI on the
// imported session.
func (o OpenCode) SynthesizeHandoff(home, workspace string, traj *harness.CanonicalTrajectory) ([]string, string, error) {
	now := time.Now().UTC()
	sid := ocID("ses", now, 0)
	uid, aid := ocID("msg", now, 1), ocID("msg", now, 2)
	ms := now.UnixMilli()
	export := map[string]any{
		"info": map[string]any{"id": sid, "slug": "lydiabox-handoff", "projectID": "global", "directory": workspace,
			"title": "Handoff: " + titleFrom(traj.OriginalGoal), "version": "lydiabox-handoff",
			"time": map[string]any{"created": ms, "updated": ms + 1}},
		"messages": []map[string]any{
			{"info": map[string]any{"id": uid, "sessionID": sid, "role": "user", "agent": "build",
				"time": map[string]any{"created": ms}},
				"parts": []map[string]any{{"id": ocID("prt", now, 3), "messageID": uid, "sessionID": sid, "type": "text", "text": seedText(traj)}}},
			{"info": map[string]any{"id": aid, "sessionID": sid, "role": "assistant", "parentID": uid, "agent": "build", "mode": "build",
				"providerID": "lydiabox", "modelID": "handoff", "cost": 0,
				"path":   map[string]any{"cwd": workspace, "root": workspace},
				"tokens": map[string]any{"input": 0, "output": 0, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}},
				"time":   map[string]any{"created": ms + 1, "completed": ms + 1}},
				"parts": []map[string]any{{"id": ocID("prt", now, 4), "messageID": aid, "sessionID": sid, "type": "text", "text": ackText}}},
		},
	}
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return nil, "", err
	}
	rel := path.Join(ocImportDir, sid+".json")
	if err := writeHome(home, rel, data); err != nil {
		return nil, "", err
	}
	guestFile := path.Join(harness.GuestHome, rel)
	cmd := []string{"sh", "-c", `opencode import "$1" && exec opencode --session "$2" --prompt "$3"`,
		"opencode-handoff", guestFile, sid, ContinuePrompt}
	return cmd, sid, nil
}

// LaunchCommand implements harness.Launcher.
func (o OpenCode) LaunchCommand(opts harness.LaunchOptions) ([]string, string) {
	cmd := []string{"opencode"}
	if opts.ResumeNativeID != "" {
		cmd = append(cmd, "--session", opts.ResumeNativeID)
	}
	if opts.Model != "" {
		cmd = append(cmd, "--model", "lydiabox/"+opts.Model)
	}
	return cmd, opts.ResumeNativeID
}

// Purge implements harness.Purger.
func (o OpenCode) Purge(home, id string) error {
	s, msgs, err := o.load(home, id)
	if errors.Is(err, harness.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return purgeOC(home, s, msgs)
}
