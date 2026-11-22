//go:build unix

package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

// TestCellGuestNative runs a cell in its default guest-native mode against
// the fake InstaBox. It proves that the host does not hold or route a
// subscription: no magpie sign-in is offered, the guest LLM tunnel sends
// nothing upstream, the host does not read or write the credential files
// in the VM, and the harness configs lose the gateway settings.
func TestCellGuestNative(t *testing.T) {
	checkGoroutines(t)
	r := newCellRig(t)
	c := r.start()
	defer c.stop(t)

	if b := r.must(http.MethodGet, "/api/v1/models/gateway", "", http.StatusOK); !strings.Contains(string(b), `"mode":"guest-native"`) {
		t.Fatalf("gateway %s", b)
	}
	if b := r.must(http.MethodGet, "/api/v1/models/subscriptions", "", http.StatusOK); !strings.Contains(string(b), `"subscriptions":[]`) {
		t.Fatalf("subscriptions %s", b)
	}
	r.must(http.MethodPost, "/api/v1/models/subscriptions/logins", `{"kind":"claude"}`, http.StatusConflict)
	r.must(http.MethodPost, "/api/v1/models/providers", `{"kind":"anthropic","name":"a","apiKey":"sk-ant-api03-KEY"}`, http.StatusConflict)
	if b := r.must(http.MethodGet, "/api/v1/models", "", http.StatusOK); !strings.Contains(string(b), `"id":"opus"`) {
		t.Fatalf("native model list %s", b)
	}
	if _, err := os.Stat(filepath.Join(r.root, "cell", "state", "magpie")); err == nil {
		t.Error("the cell made a magpie state directory")
	}

	// The machine has a signed-in Claude Code and a home that an earlier
	// gateway wiring left.
	r.must(http.MethodPost, "/api/v1/fs/workspace/mkdir", `{"path":"p","parents":true}`, http.StatusCreated)
	id := r.machine()
	home := filepath.Join(r.runner.DataDir(id), "lydiabox", "home")
	const secret = "sk-ant-oat01-SUBSCRIPTION-SECRET"
	write := func(rel, data string) {
		t.Helper()
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/.credentials.json", `{"claudeAiOauth":{"accessToken":"`+secret+`"}}`)
	write(".codex/auth.json", `{"tokens":{"refresh_token":"`+secret+`"}}`)
	write(".antigravity/settings.json", `{"endpoint":"http://127.0.0.1:3425","apiKey":"lydiabox-local","theme":"dark"}`)
	if err := os.Symlink(".claude/.credentials.json", filepath.Join(home, "notes.json")); err != nil {
		t.Fatal(err)
	}

	for _, q := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/fs/home/text?path=.claude/.credentials.json", ""},
		{http.MethodGet, "/api/v1/fs/home/download?path=.codex/auth.json", ""},
		{http.MethodGet, "/api/v1/fs/home/text?path=notes.json", ""},
		{http.MethodPut, "/api/v1/fs/home/text?path=.codex/auth.json", `{"content":"x"}`},
		{http.MethodPost, "/api/v1/fs/home/rename", `{"from":".codex","to":"codex-copy"}`},
	} {
		code, b := r.call(q.method, q.path, q.body)
		if code != http.StatusForbidden || strings.Contains(string(b), secret) {
			t.Errorf("%s %s: %d %s, want 403 without the token", q.method, q.path, code, b)
		}
	}

	// A harness start wires its config natively (agy is not installed in
	// the test, so the start itself fails after the wiring).
	r.call(http.MethodPost, "/api/v1/sessions", `{"harness":"antigravity","workspaceDir":"/workspace/p"}`)
	b, err := os.ReadFile(filepath.Join(home, ".antigravity", "settings.json"))
	if err != nil || strings.Contains(string(b), "3425") || strings.Contains(string(b), "lydiabox-local") || !strings.Contains(string(b), `"theme": "dark"`) {
		t.Errorf("antigravity settings after the native wiring: %s %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json")); !strings.Contains(string(b), secret) {
		t.Errorf("the credential file changed: %s", b)
	}

	// The guest LLM tunnel refuses, and nothing reaches the upstream.
	code, body := r.guestHTTP(id, int(rpc.PortHostGateway), http.MethodGet, "/v1/models")
	if code != http.StatusForbidden {
		t.Errorf("LLM gateway from the guest: %d %s, want 403", code, body)
	}
	if p := r.upstream.paths(); len(p) != 0 {
		t.Errorf("the upstream got %v", p)
	}
}
