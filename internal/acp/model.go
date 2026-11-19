package acp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lydiabox/lydiabox/internal/harness/adapters"
)

// configOption is one session config option of session/new. A select
// option lists its values flat or in groups ({group, name, options}).
type configOption struct {
	ID           string         `json:"id"`
	Category     string         `json:"category"`
	CurrentValue any            `json:"currentValue"`
	Options      []optionChoice `json:"options"`
}

// optionChoice is a value of a select option, or a group of values.
type optionChoice struct {
	Value   string         `json:"value"`
	Options []optionChoice `json:"options"`
}

// values returns the values of the choices, groups flattened.
func values(cs []optionChoice) []string {
	var out []string
	for _, c := range cs {
		if c.Value != "" {
			out = append(out, c.Value)
		}
		out = append(out, values(c.Options)...)
	}
	return out
}

// modelMatch reports whether an option value selects model. Agents name
// a model as "model", "<provider>/model" (opencode: "lydiabox/model"), or
// as a JSON ["<provider>", "model"] pair (dsh).
func modelMatch(value, model string) bool {
	if value == model || strings.HasSuffix(value, "/"+model) {
		return true
	}
	var pair []string
	if strings.HasPrefix(value, "[") && json.Unmarshal([]byte(value), &pair) == nil && len(pair) == 2 {
		return pair[1] == model
	}
	return false
}

// modelValue returns the value of the model option that selects model;
// the gateway route (adapters.GatewayProvider) wins over another provider that
// has a model of the same name.
func modelValue(o configOption, model string) string {
	found := ""
	for _, v := range values(o.Options) {
		if !modelMatch(v, model) {
			continue
		}
		if v == adapters.GatewayProvider+"/"+model || strings.HasPrefix(v, `["`+adapters.GatewayProvider+`",`) {
			return v
		}
		if found == "" {
			found = v
		}
	}
	return found
}

// modelChoice returns the config option and the value that select model,
// or "" when no model option lists it or it is the current value already.
func modelChoice(opts []configOption, model string) (configID, value string) {
	if model == "" {
		return "", ""
	}
	for _, o := range opts {
		if o.Category != "model" && o.ID != "model" {
			continue
		}
		v := modelValue(o, model)
		if v == "" {
			continue
		}
		if cur, ok := o.CurrentValue.(string); ok && cur == v {
			return "", ""
		}
		return o.ID, v
	}
	return "", ""
}

// setModel selects the model of the session when the agent lists it as a
// model config option.
func (b *Bridge) setModel(ctx context.Context, opts []configOption) {
	if id, v := modelChoice(opts, b.opts.Model); id != "" {
		_ = b.conn.Call(ctx, "session/set_config_option", map[string]any{"sessionId": b.session, "configId": id, "value": v}, nil)
	}
}
