package adapters

import (
	"bytes"
	"database/sql"
	"errors"
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

func (a Antigravity) meta(home, id string) (harness.SessionMeta, int, error) {
	steps, err := agySteps(home, id)
	if err != nil {
		return harness.SessionMeta{}, 0, err
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
	var goal firstPrompt
	msgs := 0
	for _, s := range steps {
		switch s.Type {
		case agyStepUserInput, agyStepPlanner:
			text := strings.TrimSpace(longest(s.Strings))
			if text == "" {
				continue
			}
			if s.Type == agyStepUserInput {
				goal.user(text)
			}
			msgs++
		case agyStepCodeAction, agyStepViewFile:
			if m.WorkspaceDir != "" {
				continue
			}
			for _, str := range s.Strings {
				if agyPathRe.MatchString(str) {
					m.WorkspaceDir = agyWorkspaceOf(strings.TrimPrefix(str, "file://"))
					break
				}
			}
		}
	}
	if raw, err := readHomeFile(home, path.Join(agyRoot, "annotations", id+".pbtxt")); err == nil {
		if mm := agyTitleRe.FindSubmatch(raw); mm != nil {
			m.Title = strings.ReplaceAll(string(mm[1]), `\"`, `"`)
		}
	}
	if task, err := readHomeFile(home, path.Join(agyRoot, "brain", id, "task.md")); err == nil && goal == "" {
		goal.user(string(task))
		if goal != "" {
			msgs++
		}
	}
	if m.Title == "" {
		m.Title = titleFrom(goal)
	}
	return m, msgs, nil
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
		m, msgs, err := a.meta(home, id)
		if err != nil || msgs == 0 {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// LaunchCommand implements harness.Launcher.
// --dangerously-skip-permissions approves every tool permission request.
func (a Antigravity) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	cmd := []string{"agy", "--dangerously-skip-permissions"}
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
