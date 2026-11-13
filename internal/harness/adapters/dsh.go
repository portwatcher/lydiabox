package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
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
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
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

func (d DSH) scan(home, rel string) (*harness.SessionMeta, error) {
	data, err := readDSH(home, rel)
	if err != nil {
		return nil, err
	}
	s := &harness.SessionMeta{Harness: d.Name(), Files: []string{hostPath(home, path.Dir(rel))}}
	usage := harness.Usage{}
	var goal firstPrompt
	var msgs int
	err = eachLine(bytes.NewReader(data), func(line []byte) error {
		if s.NativeID == "" {
			var h dshHeader
			if json.Unmarshal(line, &h) != nil || h.Type != "session" || h.ID == "" {
				return harness.ErrNoTranscript
			}
			if h.Origin == "subagent" {
				return harness.ErrNoTranscript
			}
			s.NativeID, s.WorkspaceDir = h.ID, h.Cwd
			s.CreatedAt, s.UpdatedAt = millis(h.CreatedAt), millis(h.CreatedAt)
			return nil
		}
		var ev dshEvent
		if json.Unmarshal(line, &ev) != nil {
			return nil
		}
		if t := millis(ev.Time); t.After(s.UpdatedAt) {
			s.UpdatedAt = t
		}
		switch ev.Type {
		case "user/message":
			var m dshMessage
			if json.Unmarshal(ev.Data, &m) == nil && m.Source.Kind == "user" {
				goal.user(m.text())
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
				s.Model = strings.Trim(a.Message.Source.Provider+"/"+a.Message.Source.Model, "/")
			}
			if a.Message.text() != "" {
				msgs++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.NativeID == "" || msgs == 0 {
		return nil, harness.ErrNoTranscript
	}
	s.Title = titleFrom(goal)
	if !usage.IsZero() {
		s.Usage = &usage
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
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (d DSH) find(home, id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	files, err := d.sessionFiles(home)
	if err != nil {
		return "", err
	}
	for _, rel := range files {
		if path.Base(path.Dir(rel)) != id {
			continue
		}
		if s, err := d.scan(home, rel); err == nil && s.NativeID == id {
			return rel, nil
		}
	}
	for _, rel := range files {
		if s, err := d.scan(home, rel); err == nil && s.NativeID == id {
			return rel, nil
		}
	}
	return "", fmt.Errorf("%w: dsh session %s", harness.ErrNotFound, id)
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
	rel, err := d.find(home, id)
	if errors.Is(err, harness.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return safefile.RemoveAll(home, path.Dir(rel))
}
