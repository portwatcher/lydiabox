package acp

import (
	"encoding/json"
	"testing"
)

// dshOptions are the config options of session/new of dsh 0.2.2-rc.5
// (deepseek-harness-acp 0.0.1) started with the LydiaBox overlay: the
// values are JSON [provider, model] pairs in provider groups.
const dshOptions = `[{"id":"model","name":"Model","category":"model","type":"select",
 "currentValue":"[\"lydiabox\",\"codex/gpt-6.1-sol\"]",
 "options":[{"group":"deepseek-official","name":"DeepSeek","options":[
   {"value":"[\"deepseek-official\",\"deepseek-flash\"]","name":"DeepSeek-V4.1-Flash"},
   {"value":"[\"deepseek-official\",\"gpt-6.1-mini\"]","name":"same name"}]},
  {"group":"lydiabox","name":"LydiaBox","options":[
   {"value":"[\"lydiabox\",\"codex/gpt-6.1-sol\"]","name":"codex/gpt-6.1-sol"},
   {"value":"[\"lydiabox\",\"gpt-6.1-mini\"]","name":"gpt-6.1-mini"}]}]},
 {"id":"reasoning_effort","category":"thought_level","currentValue":"high","options":[{"value":"high"}]}]`

// opencodeOptions are flat "<provider>/<model>" values.
const opencodeOptions = `[{"id":"model","category":"model","currentValue":"opencode/big-pickle",
 "options":[{"value":"opencode/big-pickle"},{"value":"lydiabox/codex/gpt-6.1-sol"}]}]`

func TestModelChoice(t *testing.T) {
	var dsh, oc []configOption
	if err := json.Unmarshal([]byte(dshOptions), &dsh); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(opencodeOptions), &oc); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		opts   []configOption
		model  string
		wantID string
		want   string
	}{
		{"dsh current model", dsh, "codex/gpt-6.1-sol", "", ""},
		{"dsh gateway route wins", dsh, "gpt-6.1-mini", "model", `["lydiabox","gpt-6.1-mini"]`},
		{"dsh other provider", dsh, "deepseek-flash", "model", `["deepseek-official","deepseek-flash"]`},
		{"dsh unknown", dsh, "kimi/kimi-k3", "", ""},
		{"opencode prefix", oc, "codex/gpt-6.1-sol", "model", "lydiabox/codex/gpt-6.1-sol"},
		{"no model", oc, "", "", ""},
	}
	for _, tc := range cases {
		id, v := modelChoice(tc.opts, tc.model)
		if id != tc.wantID || v != tc.want {
			t.Errorf("%s: (%q, %q), want (%q, %q)", tc.name, id, v, tc.wantID, tc.want)
		}
	}
}
