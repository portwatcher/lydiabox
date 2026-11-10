package wire

import (
	"fmt"
	"strings"
)

// claudeWirer edits ~/.claude/settings.json (env, model) and registers the
// MCP servers at user scope in ~/.claude.json.
type claudeWirer struct{}

const (
	claudeSettings = ".claude/settings.json"
	claudeState    = ".claude.json"
)

func (claudeWirer) Harness() string    { return "claude-code" }
func (claudeWirer) ConfigPath() string { return guestPath(claudeSettings) }

func (claudeWirer) Apply(homeDir string, b Binding) error {
	b = b.withDefaults()
	err := editJSONFile(homeDir, claudeSettings, 0o644, func(o *object) error {
		env, err := o.child("env")
		if err != nil {
			return err
		}
		if err := env.set("ANTHROPIC_BASE_URL", b.GatewayURL); err != nil {
			return err
		}
		if err := env.set("ANTHROPIC_AUTH_TOKEN", b.Token); err != nil {
			return err
		}
		if err := o.setChild("env", env); err != nil {
			return err
		}
		if b.Model != "" {
			return o.set("model", b.Model)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return editJSONFile(homeDir, claudeState, 0o600, func(o *object) error {
		servers, err := o.child("mcpServers")
		if err != nil {
			return err
		}
		err = setMCPEntries(servers, b.MCPServers, func(e *object, s MCPServer) error {
			if err := e.set("type", "http"); err != nil {
				return err
			}
			return e.set("url", s.URL)
		})
		if err != nil {
			return err
		}
		return o.setChild("mcpServers", servers)
	})
}

func (claudeWirer) CurrentModel(homeDir string) (string, error) {
	return readJSONString(homeDir, claudeSettings, "model")
}

func (claudeWirer) Env(Binding) map[string]string { return nil }

// codexWirer edits ~/.codex/config.toml line by line: the root model and
// model_provider, a [model_providers.lydiabox] table, and one
// [mcp_servers.<name>] table per MCP server.
type codexWirer struct{}

const codexConfig = ".codex/config.toml"

func (codexWirer) Harness() string    { return "codex" }
func (codexWirer) ConfigPath() string { return guestPath(codexConfig) }

func (codexWirer) Apply(homeDir string, b Binding) error {
	b = b.withDefaults()
	old, err := readOptional(homeDir, codexConfig)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(codexConfig), err)
	}
	if strings.ContainsRune(string(old), 0) {
		return fmt.Errorf("%s: %w", guestPath(codexConfig), ErrUnparseable)
	}
	doc := parseTOML(old)
	if b.Model != "" {
		doc.set("", "model", tomlString(b.Model))
	}
	doc.set("", "model_provider", tomlString(ProviderID))
	table := "model_providers." + ProviderID
	doc.set(table, "name", tomlString("LydiaBox (magpie)"))
	doc.set(table, "base_url", tomlString(b.openAIBase()))
	doc.set(table, "env_key", tomlString(TokenEnv))
	doc.set(table, "wire_api", tomlString("responses"))
	for _, s := range b.MCPServers {
		doc.set("mcp_servers."+s.Name, "url", tomlString(s.URL))
	}
	return writeIfChanged(homeDir, codexConfig, old, doc.bytes(), 0o644)
}

func (codexWirer) CurrentModel(homeDir string) (string, error) {
	old, err := readOptional(homeDir, codexConfig)
	if err != nil {
		return "", err
	}
	m, _ := parseTOML(old).getString("", "model")
	return m, nil
}

func (codexWirer) Env(b Binding) map[string]string {
	return map[string]string{TokenEnv: b.withDefaults().Token}
}
