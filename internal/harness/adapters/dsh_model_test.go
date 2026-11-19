package adapters_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lydiabox/lydiabox/internal/harness/adapters"
)

// dshGatewaySession is a dsh 0.2.2-rc.5 transcript of a turn on the
// "lydiabox" route of ~/.dsh/lydiabox.patch.yml (the source of the
// assistant message as dsh writes it).
const dshGatewaySession = `{"type":"session","version":4,"id":"6be331d7-a790-492d-9de0-5f64f13f3a75","createdAt":1794201000000,"cwd":"/workspace/lydiabox","isSeeded":false,"delegationDepth":0}
{"type":"user/message","seq":1,"time":1794201001000,"data":{"id":"um-1","role":"user","content":[{"type":"text","text":"hi"}],"source":{"kind":"user"}}}
{"type":"assistant/message","seq":2,"time":1794201002000,"data":{"turn":1,"step":1,"message":{"id":"am-1","role":"assistant","content":[{"type":"text","text":"hello"}],"source":{"kind":"model","provider":"lydiabox","model":"codex/gpt-6.1-sol"}},"stream":[],"usage":{"inputTokens":10,"outputTokens":3}}}
`

// TestDSHGatewayModel: a dsh session on the gateway route reports the
// gateway model ID without the "lydiabox/" provider prefix.
func TestDSHGatewayModel(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".dsh/sessions/---workspace-lydiabox--/6be331d7-a790-492d-9de0-5f64f13f3a75")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.v4.jsonl"), []byte(dshGatewaySession), 0o644); err != nil {
		t.Fatal(err)
	}
	metas, err := adapters.DSH{}.DiscoverSessions(home)
	if err != nil || len(metas) != 1 {
		t.Fatalf("discover: %v %+v", err, metas)
	}
	if metas[0].Model != "codex/gpt-6.1-sol" {
		t.Errorf("model %q", metas[0].Model)
	}
}
