package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// DSH reads DeepSeek Harness sessions:
// ~/.dsh/sessions/<projectKey>/<encodedId>/session[.vN].jsonl[.zstd]. The
// first line is a {type:"session"} header, then one event per line
// ({type, seq, time, data}). Files are zstd compressed by default.
type DSH struct{}

// Name implements harness.HarnessAdapter.
func (DSH) Name() string { return harness.DSH }

// dshEnvWrapper exports ~/.dsh/.env (written by the magpie wirer) into the
// dsh process, then runs the dsh command line passed as arguments.
const dshEnvWrapper = `set -a; [ -f "$HOME/.dsh/.env" ] && . "$HOME/.dsh/.env"; set +a; exec "$@"`

// dshCommand builds the wrapped command. A model given here wins over
// DSH_MODEL from ~/.dsh/.env because it is set after the file is sourced.
func dshCommand(model string, args ...string) []string {
	cmd := []string{"sh", "-c", dshEnvWrapper, "dsh"}
	if model != "" {
		cmd = append(cmd, "env", "DSH_MODEL="+model)
	}
	return append(append(cmd, "dsh"), args...)
}

type dshHeader struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CreatedAt int64  `json:"createdAt"`
	Cwd       string `json:"cwd"`
	Origin    string `json:"origin"`
}

type dshEvent struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Time int64           `json:"time"`
	Data json.RawMessage `json:"data"`
}

type dshMessage struct {
	ID         string `json:"id"`
	Role       string `json:"role"`
	ToolCallID string `json:"toolCallId"`
	IsError    bool   `json:"isError"`
	Content    []struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"content"`
	Source struct {
		Kind     string `json:"kind"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
	} `json:"source"`
}

func (m dshMessage) text() string {
	var parts []string
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// dshProjectKey encodes a cwd the way dsh names project directories:
// "--" + cwd with "/", "\" and ":" as "-" and other unsafe bytes as
// "~XXXX" + "--".
func dshProjectKey(cwd string) string {
	var b strings.Builder
	b.WriteString("--")
	for _, r := range cwd {
		switch {
		case r == '/' || r == '\\' || r == ':':
			b.WriteByte('-')
		case r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "~%04X", r)
		}
	}
	b.WriteString("--")
	return b.String()
}

// readDSH returns the decompressed JSONL of a session file.
func readDSH(home, rel string) ([]byte, error) {
	data, err := readHomeFile(home, rel)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(rel, ".zstd") && !strings.HasSuffix(rel, ".zst") {
		return data, nil
	}
	dec, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderMaxMemory(512<<20))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return io.ReadAll(dec)
}

type dshScan struct {
	meta    harness.SessionMeta
	builder *builder
}

func (d DSH) scan(home, rel string) (*dshScan, error) {
	data, err := readDSH(home, rel)
	if err != nil {
		return nil, err
	}
	s := &dshScan{meta: harness.SessionMeta{Harness: d.Name(), Files: []string{hostPath(home, path.Dir(rel))}}}
	usage := harness.Usage{}
	var b *builder
	var msgs int
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		if s.meta.NativeID == "" {
			var h dshHeader
			if json.Unmarshal(line, &h) != nil || h.Type != "session" || h.ID == "" {
				return harness.ErrNoTranscript
			}
			if h.Origin == "subagent" {
				return harness.ErrNoTranscript
			}
			s.meta.NativeID, s.meta.WorkspaceDir = h.ID, h.Cwd
			s.meta.CreatedAt, s.meta.UpdatedAt = millis(h.CreatedAt), millis(h.CreatedAt)
			b = newBuilder(h.Cwd)
			return nil
		}
		var ev dshEvent
		if json.Unmarshal(line, &ev) != nil {
			return nil
		}
		if t := millis(ev.Time); t.After(s.meta.UpdatedAt) {
			s.meta.UpdatedAt = t
		}
		switch ev.Type {
		case "working-directory/change":
			var wd struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(ev.Data, &wd) == nil && wd.Cwd != "" {
				b.workspace = wd.Cwd
			}
		case "user/message":
			var m dshMessage
			if json.Unmarshal(ev.Data, &m) == nil && m.Source.Kind == "user" {
				b.user(m.text())
				msgs++
			}
		case "assistant/message":
			var a struct {
				Message dshMessage `json:"message"`
				Usage   *struct {
					Input  int64 `json:"inputTokens"`
					Output int64 `json:"outputTokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(ev.Data, &a) != nil {
				return nil
			}
			if a.Usage != nil {
				usage.InputTokens += a.Usage.Input
				usage.OutputTokens += a.Usage.Output
			}
			if a.Message.Source.Model != "" {
				s.meta.Model = strings.Trim(a.Message.Source.Provider+"/"+a.Message.Source.Model, "/")
			}
			if t := a.Message.text(); t != "" {
				b.assistant(t)
				msgs++
			}
			for _, c := range a.Message.Content {
				if c.Type == "tool-call" {
					b.tool(c.ID, c.Name, jsonArgs(json.RawMessage(strconv.Quote(c.Arguments))))
				}
			}
		case "tool/result":
			var r struct {
				Message dshMessage `json:"message"`
			}
			if json.Unmarshal(ev.Data, &r) == nil {
				b.result(r.Message.ToolCallID, r.Message.IsError, r.Message.text())
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if b == nil || msgs == 0 {
		return nil, harness.ErrNoTranscript
	}
	s.builder = b
	s.meta.Title = titleFrom(b.goal)
	if !usage.IsZero() {
		s.meta.Usage = &usage
	}
	return s, nil
}

// sessionFiles returns the newest session file of every session directory.
func (d DSH) sessionFiles(home string) ([]string, error) {
	files, err := globHome(home, ".dsh/sessions/*/*/session*.jsonl*")
	if err != nil {
		return nil, err
	}
	newest := map[string]string{}
	newestT := map[string]time.Time{}
	for _, f := range files {
		fi, err := statHome(home, f)
		if err != nil {
			continue
		}
		dir := path.Dir(f)
		if fi.ModTime().After(newestT[dir]) || newest[dir] == "" {
			newest[dir], newestT[dir] = f, fi.ModTime()
		}
	}
	out := make([]string, 0, len(newest))
	for _, f := range newest {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, nil
}

// DiscoverSessions implements harness.HarnessAdapter.
func (d DSH) DiscoverSessions(home string) ([]harness.SessionMeta, error) {
	files, err := d.sessionFiles(home)
	if err != nil {
		return nil, err
	}
	out := []harness.SessionMeta{}
	for _, rel := range files {
		if s, err := d.scan(home, rel); err == nil {
			out = append(out, s.meta)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (d DSH) find(home, id string) (string, *dshScan, error) {
	if err := checkID(id); err != nil {
		return "", nil, err
	}
	files, err := d.sessionFiles(home)
	if err != nil {
		return "", nil, err
	}
	for _, rel := range files {
		if path.Base(path.Dir(rel)) != id {
			continue
		}
		if s, err := d.scan(home, rel); err == nil && s.meta.NativeID == id {
			return rel, s, nil
		}
	}
	for _, rel := range files {
		if s, err := d.scan(home, rel); err == nil && s.meta.NativeID == id {
			return rel, s, nil
		}
	}
	return "", nil, fmt.Errorf("%w: dsh session %s", harness.ErrNotFound, id)
}

// ParseTranscript implements harness.HarnessAdapter.
func (d DSH) ParseTranscript(home, id string) (*harness.CanonicalTrajectory, error) {
	_, s, err := d.find(home, id)
	if err != nil {
		return nil, err
	}
	t := s.builder.finish(d.Name(), id, s.meta.Model)
	t.Usage = s.meta.Usage
	return t, nil
}

// SynthesizeHandoff writes a seeded native session (header isSeeded=true
// and the handoff as the first user/message event, zstd compressed like
// dsh's own files) and resumes it in the TUI.
func (d DSH) SynthesizeHandoff(home, workspace string, traj *harness.CanonicalTrajectory) ([]string, string, error) {
	id := "session-" + newUUID()
	now := time.Now().UTC().UnixMilli()
	jsonl, err := marshalLines(
		map[string]any{"type": "session", "version": 1, "id": id, "createdAt": now, "cwd": workspace,
			"isSeeded": true, "delegationDepth": 0},
		map[string]any{"type": "user/message", "seq": 1, "time": now, "data": map[string]any{
			"id": "msg-" + newUUID(), "role": "user",
			"content": []map[string]any{{"type": "text", "text": seedText(traj)}},
			"source":  map[string]any{"kind": "user", "rpcId": "lydiabox-handoff"},
		}},
	)
	if err != nil {
		return nil, "", err
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, "", err
	}
	data := enc.EncodeAll(jsonl, nil)
	enc.Close()
	rel := path.Join(".dsh/sessions", dshProjectKey(workspace), id, "session.jsonl.zstd")
	if err := writeHome(home, rel, data); err != nil {
		return nil, "", err
	}
	return dshCommand("", "tui", "--resume", id), id, nil
}

// LaunchCommand implements harness.Launcher. dsh reads its model from the
// profile and ~/.dsh/.env (DSH_MODEL), so a per-session model is exported.
func (d DSH) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	args := []string{"tui"}
	if o.ResumeNativeID != "" {
		args = append(args, "--resume", o.ResumeNativeID)
	}
	return dshCommand(o.Model, args...), o.ResumeNativeID
}

// Purge implements harness.Purger: it removes the session directory.
func (d DSH) Purge(home, id string) error {
	rel, _, err := d.find(home, id)
	if errors.Is(err, harness.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return safefile.RemoveAll(home, path.Dir(rel))
}
