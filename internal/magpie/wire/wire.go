// Package wire points the five harness CLIs at the magpie gateway by
// editing their config files in the host copy of /home/agent. Edits are
// non-destructive (only managed keys change; TOML and .env files are edited
// line by line so comments survive) and atomic (temporary file + rename).
// Paths are resolved with internal/harness/safefile, so guest symlinks
// cannot redirect a host write outside /home/agent.
package wire

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/lydiabox/lydiabox/internal/harness/safefile"
)

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
	Apply(homeDir string, b Binding) error
	// CurrentModel returns the configured model ("" when none).
	CurrentModel(homeDir string) (string, error)
	// Env returns environment variables the harness process needs.
	Env(b Binding) map[string]string
}

// All returns one wirer per harness in display order.
func All() []Wirer {
	return []Wirer{claudeWirer{}, codexWirer{}, dshWirer{}, antigravityWirer{}, opencodeWirer{}}
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

// readOptional reads rel below homeDir; a missing file is empty.
func readOptional(homeDir, rel string) ([]byte, error) {
	b, err := safefile.ReadFile(homeDir, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// writeIfChanged writes data when it differs from old.
func writeIfChanged(homeDir, rel string, old, data []byte, perm fs.FileMode) error {
	if old != nil && string(old) == string(data) {
		return nil
	}
	if err := safefile.WriteFile(homeDir, rel, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", guestPath(rel), err)
	}
	return nil
}

// editJSONFile merges managed keys into a JSON config file.
func editJSONFile(homeDir, rel string, perm fs.FileMode, fn func(o *object) error) error {
	old, err := readOptional(homeDir, rel)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(rel), err)
	}
	data, err := editJSON(old, fn)
	if err != nil {
		return fmt.Errorf("%s: %w", guestPath(rel), err)
	}
	return writeIfChanged(homeDir, rel, old, data, perm)
}

// readJSONString returns a top-level string of a JSON config file.
func readJSONString(homeDir, rel, key string) (string, error) {
	old, err := readOptional(homeDir, rel)
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
