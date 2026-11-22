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

// dshEnvWrapper exports ~/.dsh/lydiabox.env (written by the magpie wirer)
// into the dsh process, then boots the dsh profile given as $2 with the
// app arguments that follow. A non-empty $1 is the model of the session and
// wins over DSH_MODEL from the file. dsh reads ~/.dsh/.env itself (and
// refuses base URLs there).
//
// dsh 0.2.2 reads no model variable: each profile takes its model from a
// profile row (the acp row of the acp profile, agent-default-model of the
// others), which ships deepseek-official/deepseek-v4-flash. When a model is
// known, the wrapper adds the per-launch overlay ~/.dsh/lydiabox.patch.yml
// (also written by the wirer). The overlay adds the "lydiabox" route to the
// gateway and selects process.env.DSH_MODEL in those rows. Launcher flags
// such as --patch come before the app arguments.
const dshEnvWrapper = `set -a; [ -f "$HOME/.dsh/lydiabox.env" ] && . "$HOME/.dsh/lydiabox.env"; set +a
if [ -n "$1" ]; then DSH_MODEL=$1; export DSH_MODEL; fi
DSH_PERMISSION_MODE=danger-full-access; export DSH_PERMISSION_MODE
profile=$2; shift 2
patch="$HOME/.dsh/lydiabox.patch.yml"
if [ -n "$DSH_MODEL" ] && [ -s "$patch" ]; then exec dsh "$profile" --patch "$patch" "$@"; fi
exec dsh "$profile" "$@"`

// dshCommand builds the wrapped command for a dsh profile ("tui", "acp")
// and its app arguments. A model given here wins over DSH_MODEL from
// ~/.dsh/lydiabox.env because the wrapper sets it after the file is
// sourced.
//
// dsh has no permission flag. Its base profile (@deepseek-ai/dsh-base)
// reads DSH_PERMISSION_MODE: "danger-full-access" turns off the command
// sandbox and sets the approval policy to "never", so no tool waits for an
// approval.
func dshCommand(model, profile string, args ...string) []string {
	return append([]string{"sh", "-c", dshEnvWrapper, "dsh", model, profile}, args...)
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

// maxDSHDecoded bounds the decompressed size of one session file.
var maxDSHDecoded int64 = safefile.MaxReadBytes

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
	// The guest writes these files: bound the decompressed size too.
	out, err := io.ReadAll(io.LimitReader(dec, maxDSHDecoded+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxDSHDecoded {
		return nil, fmt.Errorf("%s: %w", rel, safefile.ErrTooLarge)
	}
	return out, nil
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
				// The overlay route "lydiabox" carries gateway model IDs.
				s.Model = harness.GatewayModel(strings.Trim(a.Message.Source.Provider+"/"+a.Message.Source.Model, "/"))
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

// LaunchCommand implements harness.Launcher. The model of the session
// reaches dsh through the wrapper and the overlay (see dshEnvWrapper).
func (d DSH) LaunchCommand(o harness.LaunchOptions) ([]string, string) {
	var args []string
	if o.ResumeNativeID != "" {
		args = append(args, "--resume", o.ResumeNativeID)
	}
	return dshCommand(o.Model, "tui", args...), o.ResumeNativeID
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
