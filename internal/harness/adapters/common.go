// Package adapters implements harness.Adapter for the five supported
// harness CLIs against their on-disk session formats. Formats, sources, and
// what is verified or inferred are listed in docs/harness-formats.md.
//
// Every adapter reads and writes only below the host copy of /home/agent
// through internal/harness/safefile, so guest symlinks cannot redirect host
// reads or writes.
package adapters

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// ContinuePrompt is the first instruction a target harness gets after a
// hot-switch.
const ContinuePrompt = "Continue the interrupted task. Read .agent/HANDOFF.md first: it has the original goal, " +
	"the modified files, the recent tool actions, and the open issues from the previous agent."

// ackText is the synthetic assistant turn of a seed session.
const ackText = "I have read the handoff in .agent/HANDOFF.md and will continue the task from where the previous agent stopped."

// All returns one adapter per harness in display order.
func All() []harness.Adapter {
	return []harness.Adapter{Claude{}, Codex{}, DSH{}, Antigravity{}, OpenCode{}}
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// checkID rejects session IDs that could be used as path components to
// escape the session directory.
func checkID(id string) error {
	if !idRe.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("%w: invalid session ID %q", harness.ErrBadRequest, id)
	}
	return nil
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// newUUIDv7 returns a time-ordered UUID (codex uses UUIDv7 session IDs).
func newUUIDv7(t time.Time) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	ms := uint64(t.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*i))
	}
	b[6] = b[6]&0x0f | 0x70
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// seedText is the user message of a seed session.
func seedText(traj *harness.CanonicalTrajectory) string {
	if traj.HandoffMarkdown == "" {
		return ContinuePrompt
	}
	return ContinuePrompt + "\n\n" + traj.HandoffMarkdown
}

// lines splits JSONL content into non-empty lines.
func eachLine(r io.Reader, fn func(line []byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			if ferr := fn(t); ferr != nil {
				return ferr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readHomeFile reads rel (slash separated) below the host home directory.
func readHomeFile(home, rel string) ([]byte, error) { return safefile.ReadFile(home, rel) }

// globHome lists files below home matching pattern (slash separated,
// relative). Matches that resolve outside home are dropped.
func globHome(home, pattern string) ([]string, error) {
	root, err := safefile.Resolve(home, "")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	matches, err := fs.Glob(os.DirFS(root), pattern)
	if err != nil {
		return nil, err
	}
	out := matches[:0]
	for _, m := range matches {
		if _, err := safefile.Resolve(home, m); err == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// statHome stats rel below home.
func statHome(home, rel string) (fs.FileInfo, error) {
	p, err := safefile.Resolve(home, rel)
	if err != nil {
		return nil, err
	}
	return os.Stat(p)
}

// hostPath returns the resolved host path of rel below home.
func hostPath(home, rel string) string {
	p, err := safefile.Resolve(home, rel)
	if err != nil {
		return ""
	}
	return p
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func millis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// titleFrom shortens a first prompt into a session title.
func titleFrom(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return s
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// jsonArgs decodes tool arguments given as an object or a JSON string.
func jsonArgs(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		return m
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if json.Unmarshal([]byte(s), &m) == nil {
			return m
		}
		return map[string]any{"input": s}
	}
	return map[string]any{}
}

// writeHome atomically writes a file below the host home directory.
func writeHome(home, rel string, data []byte) error {
	return safefile.WriteFile(home, rel, data, 0o600)
}

// marshalLines renders values as JSONL.
func marshalLines(values ...any) ([]byte, error) {
	var b bytes.Buffer
	for _, v := range values {
		line, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}
