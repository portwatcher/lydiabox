package adapters

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// Antigravity reads Google Antigravity conversations:
// ~/.gemini/antigravity/conversations/<uuid>.db (SQLite; table "steps"
// holds one protobuf "step_payload" per step), the plan artifacts in
// brain/<uuid>/ (task.md, implementation_plan.md, walkthrough.md), and the
// text-protobuf annotations/<uuid>.pbtxt. The protobuf schema is not
// public: payloads are decoded with a schema-free wire-format walker and
// classified by step type (see docs/harness-formats.md).
type Antigravity struct{}

// Name implements harness.HarnessAdapter.
func (Antigravity) Name() string { return harness.Antigravity }

const agyRoot = ".gemini/antigravity"

// Step types of the Antigravity (Cascade) trajectory, inferred.
const (
	agyStepCodeAction = 5
	agyStepViewFile   = 8
	agyStepUserInput  = 14
	agyStepPlanner    = 15
	agyStepRunCommand = 21
)

// protoStrings returns the UTF-8 strings of a protobuf message in wire
// order, descending into nested messages. Byte fields that are not valid
// text and do not parse as messages are skipped.
func protoStrings(b []byte, depth int) []string {
	var out []string
	for len(b) > 0 {
		key, n := uvarint(b)
		if n <= 0 {
			return out
		}
		b = b[n:]
		switch key & 7 {
		case 0:
			_, n = uvarint(b)
			if n <= 0 {
				return out
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return out
			}
			b = b[8:]
		case 5:
			if len(b) < 4 {
				return out
			}
			b = b[4:]
		case 2:
			l, n := uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return out
			}
			field := b[n : n+int(l)]
			b = b[n+int(l):]
			if depth < 8 && looksLikeMessage(field) {
				if inner := protoStrings(field, depth+1); len(inner) > 0 {
					out = append(out, inner...)
					continue
				}
			}
			if isText(field) {
				out = append(out, string(field))
			}
		default:
			return out
		}
	}
	return out
}

func uvarint(b []byte) (uint64, int) {
	var x uint64
	for i := 0; i < len(b) && i < 10; i++ {
		x |= uint64(b[i]&0x7f) << (7 * i)
		if b[i] < 0x80 {
			return x, i + 1
		}
	}
	return 0, 0
}

// looksLikeMessage reports whether b parses completely as protobuf fields.
func looksLikeMessage(b []byte) bool {
	if len(b) == 0 || isText(b) && !bytes.ContainsAny(b[:1], "\x08\x0a\x10\x12\x18\x1a\x20\x22") {
		return false
	}
	for len(b) > 0 {
		key, n := uvarint(b)
		if n <= 0 || key>>3 == 0 {
			return false
		}
		b = b[n:]
		switch key & 7 {
		case 0:
			if _, n = uvarint(b); n <= 0 {
				return false
			}
			b = b[n:]
		case 1, 5:
			w := 8
			if key&7 == 5 {
				w = 4
			}
			if len(b) < w {
				return false
			}
			b = b[w:]
		case 2:
			l, n := uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return false
			}
			b = b[n+int(l):]
		default:
			return false
		}
	}
	return true
}

func isText(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}
	for _, r := range string(b) {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\r' {
			return false
		}
	}
	return true
}

func longest(ss []string) string {
	best := ""
	for _, s := range ss {
		if len(s) > len(best) {
			best = s
		}
	}
	return best
}

var agyPathRe = regexp.MustCompile(`^(?:/|file://)\S+\.\w+$`)

type agyStep struct {
	Type    int64
	Status  int64
	Strings []string
}

func agyOpen(home, id string) (*sql.DB, error) {
	rel := path.Join(agyRoot, "conversations", id+".db")
	p, err := safefile.Resolve(home, rel)
	if err != nil {
		return nil, err
	}
	if _, err := statHome(home, rel); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("mode", "ro")
	return sql.Open("sqlite", "file:"+filepath.ToSlash(p)+"?"+q.Encode())
}

func agySteps(home, id string) ([]agyStep, error) {
	db, err := agyOpen(home, id)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT * FROM steps ORDER BY idx`)
	if err != nil {
		return nil, err
	}
	ms, err := rowsAsMaps(rows)
	if err != nil {
		return nil, err
	}
	out := make([]agyStep, 0, len(ms))
	for _, m := range ms {
		payload, _ := m["step_payload"].([]byte)
		out = append(out, agyStep{Type: colInt(m, "step_type"), Status: colInt(m, "status"), Strings: protoStrings(payload, 0)})
	}
	return out, nil
}

var agyTitleRe = regexp.MustCompile(`(?m)^\s*title:\s*"((?:[^"\\]|\\.)*)"`)

func (a Antigravity) meta(home, id string) (harness.SessionMeta, *builder, error) {
	steps, err := agySteps(home, id)
	if err != nil {
		return harness.SessionMeta{}, nil, err
	}
	m := harness.SessionMeta{Harness: a.Name(), NativeID: id}
	for _, rel := range []string{path.Join(agyRoot, "conversations", id+".db"), path.Join(agyRoot, "brain", id),
		path.Join(agyRoot, "annotations", id+".pbtxt")} {
		if fi, err := statHome(home, rel); err == nil {
			m.Files = append(m.Files, hostPath(home, rel))
			t := fi.ModTime().UTC()
			if m.CreatedAt.IsZero() || t.Before(m.CreatedAt) {
				m.CreatedAt = t
			}
			if t.After(m.UpdatedAt) {
				m.UpdatedAt = t
			}
		}
	}
	b := newBuilder("")
	for i, s := range steps {
		switch s.Type {
		case agyStepUserInput:
			b.user(longest(s.Strings))
		case agyStepPlanner:
			b.assistant(longest(s.Strings))
		case agyStepRunCommand:
			cmd := ""
			for _, str := range s.Strings {
				if strings.TrimSpace(str) != "" && !agyPathRe.MatchString(str) {
					cmd = str
					break
				}
			}
			call := fmt.Sprintf("step-%d", i)
			b.tool(call, "run_command", map[string]any{"command": cmd})
			b.result(call, s.Status == 3, "")
		case agyStepCodeAction, agyStepViewFile:
			file := ""
			for _, str := range s.Strings {
				if agyPathRe.MatchString(str) {
					file = strings.TrimPrefix(str, "file://")
					break
				}
			}
			if b.workspace == "" && file != "" {
				b.workspace = agyWorkspaceOf(file)
			}
			name := "view_file"
			if s.Type == agyStepCodeAction {
				name = "edit_file"
			}
			b.tool("", name, map[string]any{"path": file})
		}
	}
	m.WorkspaceDir = b.workspace
	if raw, err := readHomeFile(home, path.Join(agyRoot, "annotations", id+".pbtxt")); err == nil {
		if mm := agyTitleRe.FindSubmatch(raw); mm != nil {
			m.Title = strings.ReplaceAll(string(mm[1]), `\"`, `"`)
		}
	}
	if task, err := readHomeFile(home, path.Join(agyRoot, "brain", id, "task.md")); err == nil && b.goal == "" {
		b.user(string(task))
	}
	if m.Title == "" {
		m.Title = titleFrom(b.goal)
	}
	// Re-relativize edits against the workspace found from the paths.
	if b.workspace != "" {
		for i, f := range b.modified {
			b.modified[i] = relPath(f, b.workspace)
		}
		for i, act := range b.actions {
			if verb, p, ok := strings.Cut(act, " "); ok && (verb == "edit" || verb == "read") {
				b.actions[i] = verb + " " + relPath(p, b.workspace)
			}
		}
	}
	return m, b, nil
}

// agyWorkspaceOf guesses the project directory of a file path: the first
// two segments below /workspace or /home/agent.
func agyWorkspaceOf(file string) string {
	for _, root := range []string{harness.GuestWorkspace, harness.GuestHome} {
		if rest, ok := strings.CutPrefix(file, root+"/"); ok {
			if first, _, ok := strings.Cut(rest, "/"); ok {
				return root + "/" + first
			}
		}
	}
	return path.Dir(file)
}

// DiscoverSessions implements harness.HarnessAdapter.
func (a Antigravity) DiscoverSessions(home string) ([]harness.SessionMeta, error) {
	files, err := globHome(home, agyRoot+"/conversations/*.db")
	if err != nil {
		return nil, err
	}
	out := []harness.SessionMeta{}
	for _, rel := range files {
		id := strings.TrimSuffix(path.Base(rel), ".db")
		if checkID(id) != nil {
			continue
		}
		m, b, err := a.meta(home, id)
		if err != nil || len(b.messages) == 0 {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// ParseTranscript implements harness.HarnessAdapter.
func (a Antigravity) ParseTranscript(home, id string) (*harness.CanonicalTrajectory, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	m, b, err := a.meta(home, id)
	if err != nil {
		return nil, fmt.Errorf("%w: antigravity conversation %s: %v", harness.ErrNotFound, id, err)
	}
	if len(b.messages) == 0 {
		return nil, harness.ErrNoTranscript
	}
	return b.finish(a.Name(), id, m.Model), nil
}

// SynthesizeHandoff starts agy with the continuation prompt. Antigravity
// conversations are protobuf in SQLite without a public schema, so no
// native seed is written; the handoff reaches the agent through the prompt
// and the managed block in GEMINI.md and AGENTS.md. Antigravity assigns the
// conversation ID at runtime, so newSessionID is empty.
func (a Antigravity) SynthesizeHandoff(home, workspace string, traj *harness.CanonicalTrajectory) ([]string, string, error) {
	return []string{"agy", "--prompt-interactive", ContinuePrompt}, "", nil
}

// LaunchCommand implements harness.Launcher.
func (a Antigravity) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	cmd := []string{"agy"}
	if o.ResumeNativeID != "" {
		cmd = append(cmd, "--conversation", o.ResumeNativeID)
	}
	if o.Model != "" {
		cmd = append(cmd, "--model", o.Model)
	}
	return cmd, o.ResumeNativeID
}

// Purge implements harness.Purger.
func (a Antigravity) Purge(home, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	var errs []error
	for _, rel := range []string{"conversations/" + id + ".db", "conversations/" + id + ".db-wal", "conversations/" + id + ".db-shm",
		"brain/" + id, "annotations/" + id + ".pbtxt"} {
		if err := safefile.RemoveAll(home, path.Join(agyRoot, rel)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
