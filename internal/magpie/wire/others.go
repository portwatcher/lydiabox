package wire

import (
	"strings"
)

// antigravityWirer edits ~/.antigravity/settings.json (the design path:
// gateway endpoint and model), the Antigravity CLI settings
// ~/.gemini/antigravity-cli/settings.json, and registers the MCP servers in
// the Antigravity mcp_config.json files.
//
// The CLI keys are from the published schema
// https://antigravity.google/schemas/v1/settings.json and were checked with
// agy 1.5.4 against a capture server: with "modelProvider": "gemini",
// GEMINI_API_KEY, and GOOGLE_GEMINI_BASE_URL (see Env), `agy --model <m>`
// with the custom model {"modelName": "gemini-api://<m>"} sends
// POST <base>/v1beta/models/<m>:streamGenerateContent. The wirer does not
// write the CLI "model" key: agy 1.5.4 resolves a saved custom-model label
// to an empty model ("tModel: model is empty"); the launcher passes
// --model instead.
type antigravityWirer struct{}

const (
	antigravitySettings    = ".antigravity/settings.json"
	antigravityCLISettings = ".gemini/antigravity-cli/settings.json"
)

// antigravityMCPConfigs are the design path and the documented global path.
var antigravityMCPConfigs = []string{".gemini/antigravity/mcp_config.json", ".gemini/config/mcp_config.json"}

func (antigravityWirer) Harness() string    { return "antigravity" }
func (antigravityWirer) ConfigPath() string { return guestPath(antigravitySettings) }

func (w antigravityWirer) Apply(h Home, b Binding) error {
	b = b.withDefaults()
	if b.Native {
		return w.applyNative(h, b)
	}
	err := editJSONFile(h, antigravitySettings, 0o644, func(o *object) error {
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
	if err := editJSONFile(h, antigravityCLISettings, 0o644, func(o *object) error {
		return applyAntigravityCLI(o, b.Model)
	}); err != nil {
		return err
	}
	return w.applyMCP(h, b)
}

// applyMCP registers the MCP servers in the Antigravity mcp_config.json
// files.
func (antigravityWirer) applyMCP(h Home, b Binding) error {
	for _, rel := range antigravityMCPConfigs {
		err := editJSONFile(h, rel, 0o644, func(o *object) error {
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

// applyAntigravityCLI sets the Gemini API provider and, when a model is
// bound, a custom model entry keyed by the model name, so that
// `agy --model <model>` reaches the gateway with that model.
func applyAntigravityCLI(o *object, model string) error {
	if err := o.set("modelProvider", "gemini"); err != nil {
		return err
	}
	if model == "" {
		return nil
	}
	cfg, err := o.child("customModelsConfig")
	if err != nil {
		return err
	}
	models, err := cfg.child("customModels")
	if err != nil {
		return err
	}
	entry, err := models.child(model)
	if err != nil {
		return err
	}
	if err := entry.set("modelName", "gemini-api://"+model); err != nil {
		return err
	}
	if err := entry.set("label", model); err != nil {
		return err
	}
	if err := models.setChild(model, entry); err != nil {
		return err
	}
	if err := cfg.setChild("customModels", models); err != nil {
		return err
	}
	return o.setChild("customModelsConfig", cfg)
}

func (antigravityWirer) CurrentModel(h Home) (string, error) {
	return readJSONString(h, antigravitySettings, "model")
}

func (antigravityWirer) Env(b Binding) map[string]string {
	if b.Native {
		return nil
	}
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

func (w opencodeWirer) Apply(h Home, b Binding) error {
	b = b.withDefaults()
	if b.Native {
		return w.applyNative(h, b)
	}
	return editJSONFile(h, opencodeConfig, 0o644, func(o *object) error {
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
	models, err := p.child("models")
	if err != nil {
		return err
	}
	if b.Model != "" {
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
	}
	if len(models.keys) == 0 {
		return nil
	}
	for _, id := range models.keys {
		m, err := models.child(id)
		if err != nil {
			return err
		}
		if err := opencodeImageInput(m); err != nil {
			return err
		}
		if err := models.setChild(id, m); err != nil {
			return err
		}
	}
	return p.setChild("models", models)
}

// opencodeImageInput declares image input on a gateway model entry that
// does not declare its modalities. opencode 1.18 gives a model of a custom
// provider image input only from modalities.input; without it, an image in
// a tool result (lydiabox_computer_screenshot) does not reach the model.
// The gateway catalog has no modalities, so every model gets images, as
// Claude Code and Codex send them; an entry with modalities is kept.
func opencodeImageInput(m *object) error {
	if _, ok := m.get("modalities"); ok {
		return nil
	}
	if err := m.set("modalities", map[string][]string{"input": {"text", "image"}, "output": {"text"}}); err != nil {
		return err
	}
	if _, ok := m.get("attachment"); !ok {
		return m.set("attachment", true)
	}
	return nil
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

func (opencodeWirer) CurrentModel(h Home) (string, error) {
	m, err := readJSONString(h, opencodeConfig, "model")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(m, ProviderID+"/"), nil
}

func (opencodeWirer) Env(Binding) map[string]string { return nil }
