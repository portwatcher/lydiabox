package wire

import (
	"fmt"
	"strings"
)

// Guest-native mode (docs/design.md section 4.4.2): the harnesses sign in
// themselves inside the VM and send model traffic directly to the
// provider. The wirers then remove the gateway settings that they wrote
// before, and keep only what holds no credential: the MCP servers, the
// instruction block, the permission settings, and a native model.
//
// A native wirer never reads or writes a file that a harness keeps a
// credential in (CredentialFiles), and it does not read ~/.claude.json
// (Claude Code can keep account data there). Claude Code gets its MCP
// servers from the launch command instead (harness adapters).

// CredentialFiles are the files, relative to the home directory, where
// the harnesses keep sign-ins and API keys. In guest-native mode the host
// does not read or write them (agentfs.Guard).
var CredentialFiles = []string{
	".claude/.credentials.json",
	claudeState,
	".codex/auth.json",
	".local/share/opencode/auth.json",
	".gemini/oauth_creds.json",
	".gemini/google_accounts.json",
	dshUserEnv,
}

// CredentialDirs are directories below which every file is a credential
// file in guest-native mode.
var CredentialDirs = []string{
	".config/gcloud",
	".gemini/antigravity-cli/auth",
}

// nativeModel returns model when it can be a native model ID of a harness
// that names models without a provider ("opus", "gpt-6"); a gateway model
// ID ("provider/model") is dropped.
func nativeModel(model string) string {
	if strings.Contains(model, "/") {
		return ""
	}
	return model
}

// isGatewayURL reports whether u is the gateway root or its /v1 base.
func isGatewayURL(u string, b Binding) bool {
	u = strings.TrimRight(u, "/")
	return u == b.GatewayURL || u == b.openAIBase() || u == strings.TrimRight(DefaultGatewayURL, "/")
}

// removeManaged removes key from o when its string value is one that the
// gateway wiring wrote.
func removeManaged(o *object, key string, managed func(string) bool) {
	if _, ok := o.get(key); ok && managed(o.getString(key)) {
		o.remove(key)
	}
}

func (claudeWirer) applyNative(h Home, b Binding) error {
	return editJSONFile(h, claudeSettings, 0o644, func(o *object) error {
		if _, ok := o.get("env"); ok {
			env, err := o.child("env")
			if err != nil {
				return err
			}
			removeManaged(env, "ANTHROPIC_BASE_URL", func(v string) bool { return isGatewayURL(v, b) })
			removeManaged(env, "ANTHROPIC_AUTH_TOKEN", func(v string) bool { return v == b.Token })
			if err := o.setChild("env", env); err != nil {
				return err
			}
		}
		removeManaged(o, "model", func(v string) bool { return nativeModel(v) == "" })
		if err := o.set("skipDangerousModePermissionPrompt", true); err != nil {
			return err
		}
		if m := nativeModel(b.Model); m != "" {
			return o.set("model", m)
		}
		return nil
	})
}

func (codexWirer) applyNative(h Home, b Binding) error {
	old, err := readOptional(h, codexConfig)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(codexConfig), err)
	}
	if strings.ContainsRune(string(old), 0) {
		return fmt.Errorf("%s: %w", guestPath(codexConfig), ErrUnparseable)
	}
	doc := parseTOML(old)
	if p, ok := doc.getString("", "model_provider"); ok && p == ProviderID {
		doc.remove("", "model_provider")
		if m, ok := doc.getString("", "model"); ok && nativeModel(m) == "" {
			doc.remove("", "model")
		}
	}
	doc.removeTable("model_providers." + ProviderID)
	if m := nativeModel(b.Model); m != "" {
		doc.set("", "model", tomlString(m))
	}
	for _, s := range b.MCPServers {
		doc.set("mcp_servers."+s.Name, "url", tomlString(s.URL))
		doc.set("mcp_servers."+s.Name, "default_tools_approval_mode", tomlString("approve"))
	}
	return writeIfChanged(h, codexConfig, old, doc.bytes(), 0o644)
}

func (opencodeWirer) applyNative(h Home, b Binding) error {
	return editJSONFile(h, opencodeConfig, 0o644, func(o *object) error {
		for _, key := range []string{"provider", "providers"} {
			if _, ok := o.get(key); !ok {
				continue
			}
			providers, err := o.child(key)
			if err != nil {
				return err
			}
			providers.remove(ProviderID)
			if err := o.setChild(key, providers); err != nil {
				return err
			}
		}
		removeManaged(o, "model", func(v string) bool { return strings.HasPrefix(v, ProviderID+"/") })
		// opencode names models "<provider>/<model>" natively.
		if b.Model != "" && strings.Contains(b.Model, "/") && !strings.HasPrefix(b.Model, ProviderID+"/") {
			if err := o.set("model", b.Model); err != nil {
				return err
			}
		}
		return opencodeMCP(o, b)
	})
}

func (w antigravityWirer) applyNative(h Home, b Binding) error {
	wasWired := false
	err := editJSONFile(h, antigravitySettings, 0o644, func(o *object) error {
		wasWired = isGatewayURL(o.getString("endpoint"), b)
		removeManaged(o, "endpoint", func(v string) bool { return isGatewayURL(v, b) })
		removeManaged(o, "apiKey", func(v string) bool { return v == b.Token })
		if _, ok := o.get("env"); ok {
			env, err := o.child("env")
			if err != nil {
				return err
			}
			removeManaged(env, "GOOGLE_GEMINI_BASE_URL", func(v string) bool { return isGatewayURL(v, b) })
			removeManaged(env, "GEMINI_API_KEY", func(v string) bool { return v == b.Token })
			if err := o.setChild("env", env); err != nil {
				return err
			}
		}
		if wasWired {
			removeManaged(o, "model", func(v string) bool { return nativeModel(v) == "" })
		}
		return nil
	})
	if err != nil {
		return err
	}
	if wasWired {
		// The gateway wiring set the Gemini API provider; agy then needs a
		// key. Without it, agy uses its own Google sign-in again.
		if err := editJSONFile(h, antigravityCLISettings, 0o644, unwireAntigravityCLI); err != nil {
			return err
		}
	}
	return w.applyMCP(h, b)
}

// unwireAntigravityCLI removes the Gemini API provider and the custom
// models that applyAntigravityCLI added.
func unwireAntigravityCLI(o *object) error {
	removeManaged(o, "modelProvider", func(v string) bool { return v == "gemini" })
	if _, ok := o.get("customModelsConfig"); !ok {
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
	for _, id := range append([]string(nil), models.keys...) {
		m, err := models.child(id)
		if err != nil {
			continue
		}
		if m.getString("modelName") == "gemini-api://"+id && m.getString("label") == id {
			models.remove(id)
		}
	}
	if err := cfg.setChild("customModels", models); err != nil {
		return err
	}
	return o.setChild("customModelsConfig", cfg)
}

// applyNative empties the files that the dsh launcher reads: dsh then uses
// its own profile and the keys in ~/.dsh/.env, which the host does not
// read.
func (dshWirer) applyNative(h Home, _ Binding) error {
	old, err := readOptional(h, dshEnv)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(dshEnv), err)
	}
	if err := writeIfChanged(h, dshEnv, old, []byte(dshEnvHeader), 0o600); err != nil {
		return err
	}
	oldPatch, err := readOptional(h, dshPatch)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(dshPatch), err)
	}
	if oldPatch == nil {
		return nil
	}
	return writeIfChanged(h, dshPatch, oldPatch, []byte{}, 0o644)
}
