package adapters

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// OpenCode reads sst/opencode sessions from its SQLite database or legacy
// JSON storage (see opencode_store.go).
type OpenCode struct{}

// Name implements harness.HarnessAdapter.
func (OpenCode) Name() string { return harness.OpenCode }

func ocMeta(home string, s ocSession, msgs []ocMessage) (harness.SessionMeta, int) {
	m := harness.SessionMeta{Harness: harness.OpenCode, NativeID: s.ID, Title: s.Title, WorkspaceDir: s.Directory,
		CreatedAt: millis(s.Created), UpdatedAt: millis(s.Updated)}
	if s.FromDB {
		m.Files = []string{hostPath(home, ocDB)}
	}
	for _, rel := range s.Rel {
		m.Files = append(m.Files, hostPath(home, rel))
	}
	var goal firstPrompt
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
			if p.Type != "text" || p.Synthetic {
				continue
			}
			if msg.Role == "user" {
				goal.user(p.Text)
			}
			n++
		}
	}
	if m.Title == "" || strings.HasPrefix(m.Title, "New session - ") {
		m.Title = titleFrom(goal)
	}
	if !usage.IsZero() {
		m.Usage = &usage
	}
	return m, n
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
		m, n := ocMeta(home, s, msgs)
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

// LaunchCommand implements harness.Launcher. --auto approves every
// permission request that the configuration does not deny.
func (o OpenCode) LaunchCommand(opts harness.LaunchOptions) ([]string, string) {
	cmd := []string{"opencode", "--auto"}
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
