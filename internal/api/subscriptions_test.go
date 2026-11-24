package api_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/magpie"
	"github.com/portwatcher/lydiabox/internal/magpie/magpietest"
)

const setupToken = "sk-ant-oat01-setup-token-xyz123"

func loginOf(t *testing.T, body string) magpie.SubscriptionLogin {
	t.Helper()
	var l magpie.SubscriptionLogin
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		t.Fatalf("login body %s: %v", body, err)
	}
	return l
}

// secrets are every value that must stay in magpie on the host.
func secrets(fake *magpietest.FakeAdmin) []string {
	return append(fake.StoredTokens(), setupToken, magpietest.FakeAccessToken, magpietest.FakeRefreshToken,
		magpietest.FakeCodexRefresh, magpietest.GoodCode)
}

func TestSubscriptionRoutes(t *testing.T) {
	srv, fake, home := newModelsServer(t)
	leaks := func(where, body string) {
		t.Helper()
		for _, s := range secrets(fake) {
			if strings.Contains(body, s) {
				t.Fatalf("%s leaked %q: %s", where, s, body)
			}
		}
	}
	do := func(method, path, body string, want int, contains ...string) string {
		t.Helper()
		code, got := call(t, srv, method, path, body)
		if code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, code, got, want)
		}
		for _, c := range contains {
			if !strings.Contains(got, c) {
				t.Errorf("%s %s: %s lacks %s", method, path, got, c)
			}
		}
		leaks(method+" "+path, got)
		return got
	}

	do("GET", "/api/v1/models/subscriptions", "", 200,
		`{"kind":"claude","name":"Claude","providerId":"","enabled":false,"accounts":[],"models":[]}`, `"kind":"codex"`,
		`{"kind":"antigravity","name":"Antigravity","providerId":"","enabled":false,"accounts":[],"models":[]}`)

	// ChatGPT: device code, completed by the user on the vendor page.
	codex := loginOf(t, do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"codex"}`, 201,
		`"state":"waiting"`, `"userCode":"ABCD-1234"`, `"url":"https://auth.openai.com/codex/device"`, `"needsCode":false`))
	if !strings.Contains(strings.Join(fake.Calls, ","), "StartSignIn codex device") {
		t.Fatalf("codex sign-in must use the device code: %v", fake.Calls)
	}
	do("GET", "/api/v1/models/subscriptions/logins/"+codex.ID, "", 200, `"state":"waiting"`)
	if err := fake.CompleteDeviceSignIn(codex.ID, "me@example.com", "plus"); err != nil {
		t.Fatal(err)
	}
	do("GET", "/api/v1/models/subscriptions/logins/"+codex.ID, "", 200, `"state":"done"`, `"account":"me@example.com"`)

	// Claude: browser sign-in finished with the code the page shows.
	claude := loginOf(t, do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"claude"}`, 201,
		`"needsCode":true`, `"url":"https://claude.ai/oauth/authorize`))
	do("POST", "/api/v1/models/subscriptions/logins/"+claude.ID+"/code", `{"code":"wrong"}`, 400)
	do("POST", "/api/v1/models/subscriptions/logins/"+claude.ID+"/code", `{"code":""}`, 400)
	do("POST", "/api/v1/models/subscriptions/logins/"+claude.ID+"/code", `{"code":"`+magpietest.GoodCode+`"}`, 200,
		`"state":"done"`, `"account":"claude-user@example.com"`)

	// Antigravity: Google sign-in finished with the address of the last page.
	agy := loginOf(t, do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"antigravity"}`, 201,
		`"needsCode":true`, `"userCode":""`, `"url":"https://accounts.google.com/o/oauth2/v2/auth`))
	do("POST", "/api/v1/models/subscriptions/logins/"+agy.ID+"/code", `{"code":"http://localhost:1/oauth-callback?code=x"}`, 400)
	do("POST", "/api/v1/models/subscriptions/logins/"+agy.ID+"/code", `{"code":"`+magpietest.GoodCallback+`"}`, 200,
		`"state":"done"`, `"account":"google-user@example.com"`)
	do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"antigravity","setupToken":"x"}`, 400)

	// Claude: a setup token from `claude setup-token`.
	do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"claude","setupToken":"`+setupToken+`"}`, 201,
		`"state":"done"`, `"account":"Claude setup-token xyz123"`)
	do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"codex","setupToken":"x"}`, 400)
	do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"claude","setupToken":"a b"}`, 400)
	do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"gemini"}`, 400)

	// Sign-in IDs are checked before they reach magpie.
	do("GET", "/api/v1/models/subscriptions/logins/a.b", "", 404)
	do("GET", "/api/v1/models/subscriptions/logins/unknown", "", 404)
	pending := loginOf(t, do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"claude"}`, 201))
	do("DELETE", "/api/v1/models/subscriptions/logins/"+pending.ID, "", 204)
	do("GET", "/api/v1/models/subscriptions/logins/"+pending.ID, "", 200, `"state":"canceled"`)

	do("GET", "/api/v1/models/subscriptions", "", 200,
		`"kind":"claude","name":"Claude","providerId":"claude","enabled":true`,
		`{"user":"claude-user@example.com","plan":"max","enabled":true,"needsSignIn":false}`,
		`"models":["claude-sonnet-4-5","claude-opus-4-1"]`,
		`"kind":"codex","name":"ChatGPT","providerId":"codex","enabled":true,"accounts":[{"user":"me@example.com","plan":"plus"`)
	do("GET", "/api/v1/models/subscriptions", "", 200,
		`"kind":"antigravity","name":"Antigravity","providerId":"antigravity","enabled":true,"accounts":[{"user":"google-user@example.com","plan":"pro"`,
		`"models":["gemini-3-pro"]`)
	do("GET", "/api/v1/models/providers", "", 200, `"subscription":"claude"`, `"subscription":"codex"`, `"subscription":"antigravity"`)

	// A subscription provider takes only enabled, and is not deleted.
	do("PATCH", "/api/v1/models/providers/claude", `{"name":"x"}`, 400)
	do("PATCH", "/api/v1/models/providers/claude", `{"apiKey":"x"}`, 400)
	do("PATCH", "/api/v1/models/providers/codex", `{"enabled":false}`, 200, `"enabled":false`)
	do("PATCH", "/api/v1/models/providers/codex", `{"enabled":true}`, 200, `"enabled":true`)
	do("DELETE", "/api/v1/models/providers/claude", "", 400)

	// The harness keeps the gateway and the placeholder token.
	do("PUT", "/api/v1/models/bindings/claude-code", `{"model":"claude/claude-sonnet-4-5"}`, 200, `"model":"claude/claude-sonnet-4-5"`)
	do("PUT", "/api/v1/models/bindings/codex", `{"model":"codex/gpt-5-codex"}`, 200, `"model":"codex/gpt-5-codex"`)
	do("PUT", "/api/v1/models/bindings/antigravity", `{"model":"antigravity/gemini-3-pro"}`, 200, `"model":"antigravity/gemini-3-pro"`)
	settings, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil || !strings.Contains(string(settings), "claude/claude-sonnet-4-5") || !strings.Contains(string(settings), "lydiabox-local") {
		t.Fatalf("claude-code settings = %s, %v", settings, err)
	}
	assertNoSecretsBelow(t, home, secrets(fake))

	// Removing accounts: magpie keeps the only Claude account its own
	// Claude Code copy is signed in to.
	do("DELETE", "/api/v1/models/subscriptions/claude", "", 400)
	do("DELETE", "/api/v1/models/subscriptions/claude?account=nobody", "", 404)
	do("DELETE", "/api/v1/models/subscriptions/claude?account="+strings.ReplaceAll("Claude setup-token xyz123", " ", "%20"), "", 204)
	do("DELETE", "/api/v1/models/subscriptions/claude?account=claude-user@example.com", "", 409, `"conflict"`)
	do("DELETE", "/api/v1/models/subscriptions/codex?account=me@example.com", "", 204)
	do("DELETE", "/api/v1/models/subscriptions/codex?account=me@example.com", "", 404)
	do("DELETE", "/api/v1/models/subscriptions/antigravity?account=google-user@example.com", "", 204)
	do("GET", "/api/v1/models/subscriptions", "", 200, `{"kind":"codex","name":"ChatGPT","providerId":"","enabled":false,"accounts":[],"models":[]}`)
	if strings.Join(fake.StoredTokens(), ",") != magpietest.FakeAccessToken+","+magpietest.FakeRefreshToken {
		t.Fatalf("removed accounts must drop their tokens, have %v", fake.StoredTokens())
	}

	fake.SetDown(true)
	do("GET", "/api/v1/models/subscriptions", "", 503, `"unavailable"`)
	do("POST", "/api/v1/models/subscriptions/logins", `{"kind":"codex"}`, 503)
}

// assertNoSecretsBelow fails when a file below dir holds a secret.
func assertNoSecretsBelow(t *testing.T, dir string, secrets []string) {
	t.Helper()
	files := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files++
		for _, s := range secrets {
			if strings.Contains(string(b), s) {
				t.Errorf("%s holds the secret %q", p, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no harness config was written below the guest home")
	}
}
