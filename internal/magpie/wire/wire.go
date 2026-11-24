// Package wire points the five harness CLIs at the magpie gateway by
// editing their config files in /home/agent through a Home: the host copy
// on the JuiceFS mount (juicefs storage mode) or the guest daemon file
// methods (provider storage mode). Edits are non-destructive (only managed
// keys change; TOML and .env files are edited line by line so comments
// survive) and atomic (temporary file + rename). Paths are resolved inside
// the home root, so guest symlinks cannot redirect a write outside
// /home/agent. Only the placeholder gateway token is written; upstream LLM
// keys never reach the guest disk.
package wire

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

// Home is the guest home tree; paths are relative to /home/agent.
type Home interface {
	ReadFile(rel string) ([]byte, error)
	// WriteFile writes atomically, creates parents, and keeps the mode of
	// an existing file.
	WriteFile(rel string, data []byte, perm fs.FileMode) error
}

// DirHome is a Home on a host directory (internal/harness/safefile).
type DirHome string

// ReadFile implements Home.
func (d DirHome) ReadFile(rel string) ([]byte, error) { return safefile.ReadFile(string(d), rel) }

// WriteFile implements Home.
func (d DirHome) WriteFile(rel string, data []byte, perm fs.FileMode) error {
	return safefile.WriteFile(string(d), rel, data, perm)
}

// FSHome returns the home root of a tenant in an agentfs.FS.
func FSHome(ctx context.Context, fsys agentfs.FS, tenantID string) Home {
	return fsHome{ctx: ctx, fs: fsys, tenantID: tenantID}
}

type fsHome struct {
	ctx      context.Context
	fs       agentfs.FS
	tenantID string
}

func (f fsHome) ReadFile(rel string) ([]byte, error) {
	return f.fs.ReadFile(f.ctx, f.tenantID, agentfs.RootHome, rel)
}

func (f fsHome) WriteFile(rel string, data []byte, perm fs.FileMode) error {
	return f.fs.WriteFile(f.ctx, f.tenantID, agentfs.RootHome, rel, data, perm)
}

// Defaults of the in-VM endpoints (docs/design.md sections 4.4 and 4.5).
const (
	DefaultGatewayURL = "http://127.0.0.1:3425"
	// LocalToken is the placeholder credential harnesses send to the guest
	// gateway listener. The host side of the tunnel replaces it with the
	// real gateway key (magpie.Supervisor.InjectHeaders).
	LocalToken = "lydiabox-local"
	// ProviderID names the managed provider entry in codex and opencode.
	ProviderID = "lydiabox"
	// TokenEnv carries LocalToken to harnesses that read keys from the
	// environment (codex env_key).
	TokenEnv = "LYDIABOX_GATEWAY_TOKEN"
)

// MCPServer is one HTTP MCP endpoint registered with every harness.
type MCPServer struct {
	Name string
	URL  string
}

// DefaultMCPServers are the computer-use and connector MCP servers that
// lydiabox-guest serves inside the VM.
var DefaultMCPServers = []MCPServer{
	{Name: "lydiabox", URL: "http://127.0.0.1:8910/mcp"},
	{Name: "lydiabox-connectors", URL: "http://127.0.0.1:8911/mcp"},
}

// ErrUnparseable is returned when an existing config file cannot be parsed.
// The file is left untouched.
var ErrUnparseable = errors.New("config file cannot be parsed; it was not changed")

// Binding is what a wirer writes.
type Binding struct {
	// GatewayURL is the gateway root as seen from the guest.
	GatewayURL string
	// Token is the credential harnesses send to the gateway.
	Token string
	// Model is a magpie model ID ("provider/model" or "group/<id>"). An
	// empty model leaves the configured model unchanged.
	Model      string
	MCPServers []MCPServer
	// Native selects guest-native mode: the harness signs in itself in the
	// VM, and the wirer removes the gateway settings (native.go).
	Native bool
	// Fallback returns a model that the gateway serves ("" when none). A
	// wirer whose harness has no usable default model (dsh) calls it when
	// no model is bound and its config has none; it may be nil.
	Fallback func() string
}

func (b Binding) withDefaults() Binding {
	if b.GatewayURL == "" {
		b.GatewayURL = DefaultGatewayURL
	}
	b.GatewayURL = strings.TrimRight(b.GatewayURL, "/")
	if b.Token == "" {
		b.Token = LocalToken
	}
	if b.MCPServers == nil {
		b.MCPServers = DefaultMCPServers
	}
	return b
}

// openAIBase is the OpenAI-style base URL (".../v1").
func (b Binding) openAIBase() string { return b.GatewayURL + "/v1" }

// Wirer edits the config of one harness.
type Wirer interface {
	Harness() string
	// ConfigPath is the guest path of the file that holds the model.
	ConfigPath() string
	// Apply points the harness at the gateway and binds the model.
	Apply(h Home, b Binding) error
	// CurrentModel returns the configured model ("" when none).
	CurrentModel(h Home) (string, error)
	// Env returns environment variables the harness process needs.
	Env(b Binding) map[string]string
}

// All returns one wirer per harness in display order. Each also keeps the
// LydiaBox block in the global instruction file of its harness
// (instructions.go) and the LydiaBox skills in its skill directory
// (skills.go).
func All() []Wirer {
	ws := []Wirer{claudeWirer{}, codexWirer{}, dshWirer{}, antigravityWirer{}, opencodeWirer{}}
	for i, w := range ws {
		ws[i] = withInstructions(withSkills(w))
	}
	return ws
}

// For returns the wirer of harness.
func For(harness string) (Wirer, bool) {
	for _, w := range All() {
		if w.Harness() == harness {
			return w, true
		}
	}
	return nil, false
}

// guestPath maps a path relative to the home directory to its guest path.
func guestPath(rel string) string { return path.Join("/home/agent", rel) }

// readOptional reads rel below h; a missing file is empty.
func readOptional(h Home, rel string) ([]byte, error) {
	b, err := h.ReadFile(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// writeIfChanged writes data when it differs from old.
func writeIfChanged(h Home, rel string, old, data []byte, perm fs.FileMode) error {
	if old != nil && string(old) == string(data) {
		return nil
	}
	if err := h.WriteFile(rel, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", guestPath(rel), err)
	}
	return nil
}

// editJSONFile merges managed keys into a JSON config file.
func editJSONFile(h Home, rel string, perm fs.FileMode, fn func(o *object) error) error {
	old, err := readOptional(h, rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(rel), err)
	}
	data, err := editJSON(old, fn)
	if err != nil {
		return fmt.Errorf("%s: %w", guestPath(rel), err)
	}
	return writeIfChanged(h, rel, old, data, perm)
}

// readJSONString returns a top-level string of a JSON config file.
func readJSONString(h Home, rel, key string) (string, error) {
	old, err := readOptional(h, rel)
	if err != nil {
		return "", err
	}
	o, err := parseObject(old)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %v", guestPath(rel), ErrUnparseable, err)
	}
	return o.getString(key), nil
}

// setMCPEntries writes entry(server) under parent[name] for every server,
// keeping unrelated fields of an existing entry.
func setMCPEntries(parent *object, servers []MCPServer, entry func(e *object, s MCPServer) error) error {
	for _, s := range servers {
		e, err := parent.child(s.Name)
		if err != nil {
			return err
		}
		if err := entry(e, s); err != nil {
			return err
		}
		if err := parent.setChild(s.Name, e); err != nil {
			return err
		}
	}
	return nil
}
