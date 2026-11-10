package wire

import (
	"fmt"
	"strings"
)

// dshWirer edits ~/.dsh/.env. The dsh launcher exports this file into the
// harness process, so the base URLs apply whatever profile dsh boots.
type dshWirer struct{}

const dshEnv = ".dsh/.env"

func (dshWirer) Harness() string    { return "dsh" }
func (dshWirer) ConfigPath() string { return guestPath(dshEnv) }

func (dshWirer) Apply(homeDir string, b Binding) error {
	b = b.withDefaults()
	old, err := readOptional(homeDir, dshEnv)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(dshEnv), err)
	}
	doc := parseEnv(old)
	for _, kv := range dshValues(b) {
		doc.set(kv[0], kv[1])
	}
	if b.Model != "" {
		doc.set("DSH_MODEL", b.Model)
	}
	return writeIfChanged(homeDir, dshEnv, old, doc.bytes(), 0o600)
}

// dshValues are the managed variables, in a fixed order for stable output.
// The three base URLs are the gateway root (design section 4.4.1); the
// gateway adds /v1 to OpenAI-style paths that arrive without it.
func dshValues(b Binding) [][2]string {
	return [][2]string{
		{"OPENAI_BASE_URL", b.GatewayURL},
		{"OPENAI_API_KEY", b.Token},
		{"ANTHROPIC_BASE_URL", b.GatewayURL},
		{"ANTHROPIC_API_KEY", b.Token},
		{"DEEPSEEK_BASE_URL", b.GatewayURL},
		{"DEEPSEEK_API_KEY", b.Token},
	}
}

func (dshWirer) CurrentModel(homeDir string) (string, error) {
	old, err := readOptional(homeDir, dshEnv)
	if err != nil {
		return "", err
	}
	m, _ := parseEnv(old).get("DSH_MODEL")
	return m, nil
}

func (dshWirer) Env(Binding) map[string]string { return nil }

// antigravityWirer edits ~/.antigravity/settings.json and registers the MCP
// servers in the Antigravity mcp_config.json files.
type antigravityWirer struct{}

const antigravitySettings = ".antigravity/settings.json"

// antigravityMCPConfigs are the design path and the documented global path.
var antigravityMCPConfigs = []string{".gemini/antigravity/mcp_config.json", ".gemini/config/mcp_config.json"}

func (antigravityWirer) Harness() string    { return "antigravity" }
func (antigravityWirer) ConfigPath() string { return guestPath(antigravitySettings) }

func (w antigravityWirer) Apply(homeDir string, b Binding) error {
	b = b.withDefaults()
	err := editJSONFile(homeDir, antigravitySettings, 0o644, func(o *object) error {
		if err := o.set("endpoint", b.GatewayURL); err != nil {
			return err
		}
		if err := o.set("apiKey", b.Token); err != nil {
			return err
		}
		env, err := o.child("env")
		if err != nil {
			return err
		}
		for _, k := range []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"} {
			if err := env.set(k, w.Env(b)[k]); err != nil {
				return err
			}
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
	for _, rel := range antigravityMCPConfigs {
		err := editJSONFile(homeDir, rel, 0o644, func(o *object) error {
			servers, err := o.child("mcpServers")
			if err != nil {
				return err
			}
			err = setMCPEntries(servers, b.MCPServers, func(e *object, s MCPServer) error {
				return e.set("serverUrl", s.URL)
			})
			if err != nil {
				return err
			}
			return o.setChild("mcpServers", servers)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (antigravityWirer) CurrentModel(homeDir string) (string, error) {
	return readJSONString(homeDir, antigravitySettings, "model")
}

func (antigravityWirer) Env(b Binding) map[string]string {
	b = b.withDefaults()
	return map[string]string{"GOOGLE_GEMINI_BASE_URL": b.GatewayURL, "GEMINI_API_KEY": b.Token}
}

// opencodeWirer edits ~/.config/opencode/opencode.json: a custom
// OpenAI-compatible provider, the model, and remote MCP entries. It writes
// the long-standing "provider"/"mcp.<name>" form unless the file already
// uses the newer "providers"/"mcp.servers" form.
type opencodeWirer struct{}

const opencodeConfig = ".config/opencode/opencode.json"

func (opencodeWirer) Harness() string    { return "opencode" }
func (opencodeWirer) ConfigPath() string { return guestPath(opencodeConfig) }

func (opencodeWirer) Apply(homeDir string, b Binding) error {
	b = b.withDefaults()
	return editJSONFile(homeDir, opencodeConfig, 0o644, func(o *object) error {
		if _, ok := o.get("$schema"); !ok && len(o.keys) == 0 {
			if err := o.set("$schema", "https://opencode.ai/config.json"); err != nil {
				return err
			}
		}
		_, hasOld := o.get("provider")
		_, hasNew := o.get("providers")
		key := "provider"
		if hasNew && !hasOld {
			key = "providers"
		}
		providers, err := o.child(key)
		if err != nil {
			return err
		}
		p, err := providers.child(ProviderID)
		if err != nil {
			return err
		}
		if err := opencodeProvider(p, key == "providers", b); err != nil {
			return err
		}
		if err := providers.setChild(ProviderID, p); err != nil {
			return err
		}
		if err := o.setChild(key, providers); err != nil {
			return err
		}
		if b.Model != "" {
			if err := o.set("model", ProviderID+"/"+b.Model); err != nil {
				return err
			}
		}
		return opencodeMCP(o, b)
	})
}

func opencodeProvider(p *object, newForm bool, b Binding) error {
	if err := p.set("name", "LydiaBox (magpie)"); err != nil {
		return err
	}
	opts := "options"
	if newForm {
		opts = "settings"
		if err := p.set("package", "@opencode/ai/providers/openai-compatible"); err != nil {
			return err
		}
	} else if err := p.set("npm", "@ai-sdk/openai-compatible"); err != nil {
		return err
	}
	o, err := p.child(opts)
	if err != nil {
		return err
	}
	if err := o.set("baseURL", b.openAIBase()); err != nil {
		return err
	}
	if err := o.set("apiKey", b.Token); err != nil {
		return err
	}
	if err := p.setChild(opts, o); err != nil {
		return err
	}
	if b.Model == "" {
		return nil
	}
	models, err := p.child("models")
	if err != nil {
		return err
	}
	m, err := models.child(b.Model)
	if err != nil {
		return err
	}
	if m.getString("name") == "" {
		if err := m.set("name", b.Model); err != nil {
			return err
		}
	}
	if err := models.setChild(b.Model, m); err != nil {
		return err
	}
	return p.setChild("models", models)
}

func opencodeMCP(o *object, b Binding) error {
	mcp, err := o.child("mcp")
	if err != nil {
		return err
	}
	parent, nested := mcp, false
	if _, ok := mcp.get("servers"); ok {
		if parent, err = mcp.child("servers"); err != nil {
			return err
		}
		nested = true
	}
	err = setMCPEntries(parent, b.MCPServers, func(e *object, s MCPServer) error {
		if err := e.set("type", "remote"); err != nil {
			return err
		}
		if err := e.set("url", s.URL); err != nil {
			return err
		}
		if nested {
			e.remove("enabled")
			return e.set("disabled", false)
		}
		return e.set("enabled", true)
	})
	if err != nil {
		return err
	}
	if nested {
		if err := mcp.setChild("servers", parent); err != nil {
			return err
		}
	}
	return o.setChild("mcp", mcp)
}

func (opencodeWirer) CurrentModel(homeDir string) (string, error) {
	m, err := readJSONString(homeDir, opencodeConfig, "model")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(m, ProviderID+"/"), nil
}

func (opencodeWirer) Env(Binding) map[string]string { return nil }
