package wire

import (
	"fmt"
	"strings"
)

// dshWirer writes ~/.dsh/lydiabox.env and ~/.dsh/lydiabox.patch.yml. The
// dsh launcher of LydiaBox (internal/harness/adapters, dshEnvWrapper)
// exports the env file into the harness process, so the base URLs apply
// whatever profile dsh boots. dsh 0.2.2 refuses base URLs in its own
// ~/.dsh/.env ("only the launching environment may set" them), so the
// wirer also removes the managed keys from that file.
//
// dsh 0.2.2 reads no model variable. Each profile takes its model from a
// profile row: "acp" in the acp profile and "agent-default-model" in the
// others, both deepseek-official/deepseek-v4-flash as shipped. The launcher
// adds the patch file as a per-launch overlay (dsh <profile> --patch …)
// when DSH_MODEL is set: the overlay adds the "lydiabox" route (the
// gateway's OpenAI-compatible API) with the model in DSH_MODEL, and selects
// it in both rows. A row that a profile does not have is ignored.
type dshWirer struct{}

const (
	dshEnv     = ".dsh/lydiabox.env"
	dshUserEnv = ".dsh/.env"
	dshPatch   = ".dsh/lydiabox.patch.yml"
)

const dshEnvHeader = "# Managed by LydiaBox: the dsh launcher exports these variables.\n"

func (dshWirer) Harness() string    { return "dsh" }
func (dshWirer) ConfigPath() string { return guestPath(dshEnv) }

func (w dshWirer) Apply(h Home, b Binding) error {
	b = b.withDefaults()
	if b.Native {
		return w.applyNative(h, b)
	}
	old, err := readOptional(h, dshEnv)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(dshEnv), err)
	}
	user, err := readOptional(h, dshUserEnv)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(dshUserEnv), err)
	}
	if len(old) == 0 {
		old = nil
	}
	doc := parseEnv(old)
	if len(doc.lines) == 0 {
		doc = parseEnv([]byte(dshEnvHeader))
	}
	userDoc := parseEnv(user)
	model := b.Model
	if model == "" {
		// Keep a model bound before the move to lydiabox.env.
		if m, ok := doc.get("DSH_MODEL"); ok && m != "" {
			model = m
		} else if m, ok := userDoc.get("DSH_MODEL"); ok && m != "" {
			model = m
		} else if b.Fallback != nil {
			// No model was ever bound: use one that the gateway serves,
			// not the deepseek-v4-flash of the dsh profiles.
			model = b.Fallback()
		}
	}
	for _, kv := range dshValues(b) {
		doc.set(kv[0], kv[1])
	}
	if model != "" {
		doc.set("DSH_MODEL", model)
	}
	moved := false
	for _, kv := range append(dshValues(b), [2]string{"DSH_MODEL", ""}) {
		if userDoc.del(kv[0]) {
			moved = true
		}
	}
	if moved {
		if err := writeIfChanged(h, dshUserEnv, user, userDoc.bytes(), 0o600); err != nil {
			return err
		}
	}
	if err := writeIfChanged(h, dshEnv, old, doc.bytes(), 0o600); err != nil {
		return err
	}
	oldPatch, err := readOptional(h, dshPatch)
	if err != nil {
		return fmt.Errorf("read %s: %w", guestPath(dshPatch), err)
	}
	return writeIfChanged(h, dshPatch, oldPatch, []byte(dshPatchYAML(b)), 0o644)
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

// dshPatchYAML is the per-launch overlay. The route is a pi-ai provider
// (@deepseek-ai/dsh-llm-pi-ai): openai-completions to the gateway with the
// placeholder key in OPENAI_API_KEY, and image input on (the gateway
// catalog has no modalities; the agents of the other harnesses send
// images too). "store" is not sent. The model ID is the gateway model ID
// as is; the overlay holds no secret.
func dshPatchYAML(b Binding) string {
	return strings.Join([]string{
		"# Managed by LydiaBox: the dsh launcher adds this overlay (dsh <profile> --patch) when DSH_MODEL is set.",
		"- id: llm-pi-ai",
		"  config:",
		"    providers:",
		"      " + ProviderID + ":",
		"        displayName: LydiaBox",
		"        api: openai-completions",
		"        baseURL: " + yamlQuote(b.openAIBase()),
		"        apiKeyEnv: OPENAI_API_KEY",
		"        defaultInput: [text, image]",
		"        compat:",
		"          supportsStore: false",
		`        models: !!js "[{ id: process.env.DSH_MODEL }]"`,
		"- id: agent-default-model",
		"  config:",
		"    provider: " + ProviderID,
		"    model: !!js process.env.DSH_MODEL",
		"- id: acp",
		"  config:",
		"    provider: " + ProviderID,
		"    model: !!js process.env.DSH_MODEL",
		"",
	}, "\n")
}

// yamlQuote returns s as a YAML double-quoted scalar.
func yamlQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

func (dshWirer) CurrentModel(h Home) (string, error) {
	old, err := readOptional(h, dshEnv)
	if err != nil {
		return "", err
	}
	if m, ok := parseEnv(old).get("DSH_MODEL"); ok {
		return m, nil
	}
	// A home wired before the move to lydiabox.env.
	user, err := readOptional(h, dshUserEnv)
	if err != nil {
		return "", err
	}
	m, _ := parseEnv(user).get("DSH_MODEL")
	return m, nil
}

func (dshWirer) Env(Binding) map[string]string { return nil }
