package wire

import (
	"fmt"
	"strings"
)

// claudeWirer edits ~/.claude/settings.json (env, model,
// skipDangerousModePermissionPrompt) and registers the MCP servers at user
// scope in ~/.claude.json. It also marks the first-start steps (theme,
// security notes) as done and trusts the persistent trees, so a session
// starts at the prompt; values that the user set stay. The harness adapter
// starts Claude Code with --dangerously-skip-permissions;
// skipDangerousModePermissionPrompt accepts that mode's warning, which
// otherwise stops each session at a "No, exit / Yes, I accept" question.
type claudeWirer struct{}

const (
	claudeSettings = ".claude/settings.json"
	claudeState    = ".claude.json"
)

// claudeTrustedDirs are the folders that Claude Code trusts without its
// folder trust question; it also trusts their subfolders. The VM is the
// agent's own computer. Claude Code asks for the real path of the working
// directory, so the list has the guest paths and, for the InstaBox image,
// their link targets (docs/instabox.md, "Run-time layout").
var claudeTrustedDirs = []string{"/workspace", "/home/agent", "/data/workspace", "/data/lydiabox/home"}

// claudeFirstStart marks the first-start steps as done unless the user
// state already has them, and trusts claudeTrustedDirs.
func claudeFirstStart(o *object) error {
	if _, ok := o.get("hasCompletedOnboarding"); !ok {
		if err := o.set("hasCompletedOnboarding", true); err != nil {
			return err
		}
	}
	projects, err := o.child("projects")
	if err != nil {
		return err
	}
	for _, dir := range claudeTrustedDirs {
		p, err := projects.child(dir)
		if err != nil {
			return err
		}
		if _, ok := p.get("hasTrustDialogAccepted"); ok {
			continue
		}
		if err := p.set("hasTrustDialogAccepted", true); err != nil {
			return err
		}
		if err := projects.setChild(dir, p); err != nil {
			return err
		}
	}
	return o.setChild("projects", projects)
}

func (claudeWirer) Harness() string    { return "claude-code" }
func (claudeWirer) ConfigPath() string { return guestPath(claudeSettings) }

func (w claudeWirer) Apply(h Home, b Binding) error {
	b = b.withDefaults()
	if b.Native {
		return w.applyNative(h, b)
	}
	err := editJSONFile(h, claudeSettings, 0o644, func(o *object) error {
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
		if err := o.set("skipDangerousModePermissionPrompt", true); err != nil {
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
	return editJSONFile(h, claudeState, 0o600, func(o *object) error {
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
		if err := o.setChild("mcpServers", servers); err != nil {
			return err
		}
		return claudeFirstStart(o)
	})
}

func (claudeWirer) CurrentModel(h Home) (string, error) {
	return readJSONString(h, claudeSettings, "model")
}

func (claudeWirer) Env(Binding) map[string]string { return nil }

// codexWirer edits ~/.codex/config.toml line by line: the root model and
// model_provider, a [model_providers.lydiabox] table, and one
// [mcp_servers.<name>] table per MCP server. The MCP server tables set
// default_tools_approval_mode = "approve", so LydiaBox's own tools do not
// ask for approval also in a Codex that the user starts without the
// harness adapter's --dangerously-bypass-approvals-and-sandbox.
type codexWirer struct{}

const codexConfig = ".codex/config.toml"

func (codexWirer) Harness() string    { return "codex" }
func (codexWirer) ConfigPath() string { return guestPath(codexConfig) }

func (w codexWirer) Apply(h Home, b Binding) error {
	b = b.withDefaults()
	if b.Native {
		return w.applyNative(h, b)
	}
	old, err := readOptional(h, codexConfig)
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
		doc.set("mcp_servers."+s.Name, "default_tools_approval_mode", tomlString("approve"))
	}
	return writeIfChanged(h, codexConfig, old, doc.bytes(), 0o644)
}

func (codexWirer) CurrentModel(h Home) (string, error) {
	old, err := readOptional(h, codexConfig)
	if err != nil {
		return "", err
	}
	m, _ := parseTOML(old).getString("", "model")
	return m, nil
}

func (codexWirer) Env(b Binding) map[string]string {
	if b.Native {
		return nil
	}
	return map[string]string{TokenEnv: b.withDefaults().Token}
}
