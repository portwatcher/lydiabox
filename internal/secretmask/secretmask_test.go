package secretmask

import (
	"strings"
	"testing"
)

func TestMasksKnownFormats(t *testing.T) {
	for _, tok := range []string{
		"sk-ant-oat01-" + strings.Repeat("a", 40),
		"sk-ant-ort01-" + strings.Repeat("B", 40),
		"sk-proj-" + strings.Repeat("c", 40),
		"ya29." + strings.Repeat("d", 40),
		"1//0" + strings.Repeat("e", 40),
		"AIza" + strings.Repeat("f", 35),
		"ghp_" + strings.Repeat("g", 36),
		"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NSJ9.c2lnbmF0dXJlc2ln",
		"lbx-gw-" + strings.Repeat("0a", 24),
	} {
		in := `{"text":"token ` + tok + ` end"}`
		got := String(in)
		if strings.Contains(got, tok) || !strings.Contains(got, Mask) {
			t.Errorf("not masked: %s -> %s", in, got)
		}
		if b := Bytes([]byte(in)); string(b) != got {
			t.Errorf("Bytes and String differ: %s", b)
		}
	}
}

func TestKeepsOrdinaryText(t *testing.T) {
	for _, s := range []string{
		`{"text":"use the sk-learn package and the task-runner"}`,
		`the path is /home/agent/.codex/config.toml`,
		`see the task-management-system-overview-document page`,
		`{"model":"claude-opus-4","id":"0f9c2d8e-3b1a-4c55-9a7e-1d2f3a4b5c6d"}`,
	} {
		if got := String(s); got != s {
			t.Errorf("changed ordinary text: %s -> %s", s, got)
		}
	}
}
