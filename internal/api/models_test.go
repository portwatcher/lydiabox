package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/magpie"
	"github.com/lydiabox/lydiabox/internal/magpie/magpietest"
	"github.com/lydiabox/lydiabox/internal/store"
)

type homeDir string

func (h homeDir) HomeDir(string) (string, error) { return string(h), nil }

func (h homeDir) WorkspaceDir(string) (string, error) { return string(h) + "-workspace", nil }

func newModelsServer(t *testing.T) (*httptest.Server, *magpietest.FakeAdmin, string) {
	t.Helper()
	fake := magpietest.NewFakeAdmin()
	home := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	svc := &magpie.Service{Gateway: magpietest.Gateway{Fake: fake}, Bindings: &magpie.Bindings{Store: st, Files: agentfs.NewLocal(homeDir(home), agentfs.LocalOptions{})}}
	auth := api.NewAuthenticator(token)
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, api.ModelsRoutes{Service: svc})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, fake, home
}

func call(t *testing.T, srv *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestModelsRoutes(t *testing.T) {
	srv, fake, home := newModelsServer(t)
	steps := []struct {
		method, path, body string
		code               int
		contains           []string
	}{
		{"GET", "/api/v1/models/gateway", "", 200, []string{`"status":"running"`, `"addr":"127.0.0.1:3425"`, `"version":"0.1.3531"`}},
		{"GET", "/api/v1/models/providers", "", 200, []string{`"providers":[]`}},
		{"POST", "/api/v1/models/providers", `{"kind":"openai","name":"OpenAI","baseUrl":"","apiKey":"sk-secret-1"}`, 201,
			[]string{`"id":"openai"`, `"kind":"openai"`, `"hasKey":true`, `"enabled":true`, `"status":"ok"`, `"models":["openai-large","openai-small"]`}},
		{"POST", "/api/v1/models/providers", `{"kind":"openai","name":"OpenAI","apiKey":"x"}`, 409, []string{`"conflict"`}},
		{"POST", "/api/v1/models/providers", `{"kind":"deepseek","name":"My DeepSeek","baseUrl":"https://api.deepseek.com/v1"}`, 201,
			[]string{`"id":"my-deepseek"`, `"kind":"openai-compatible"`, `"baseUrl":"https://api.deepseek.com/v1"`, `"hasKey":false`}},
		{"POST", "/api/v1/models/providers", `{"kind":"x","baseUrl":"ftp://bad"}`, 400, nil},
		{"POST", "/api/v1/models/providers", `{"name":"no kind"}`, 400, nil},
		{"PATCH", "/api/v1/models/providers/my-deepseek", `{"apiKey":"sk-secret-2","enabled":false}`, 200,
			[]string{`"hasKey":true`, `"enabled":false`, `"status":"disabled"`}},
		{"PATCH", "/api/v1/models/providers/missing", `{"enabled":false}`, 404, nil},
		{"GET", "/api/v1/models", "", 200, []string{`{"id":"openai/openai-large","provider":"openai","name":"openai-large","contextWindow":128000}`}},
		{"PUT", "/api/v1/models/routing", `{"groups":[{"name":"budget","strategy":"fallback","targets":["openai/openai-small","my-deepseek/my-deepseek-large"]},{"name":"Spread","strategy":"round-robin","targets":["openai/openai-large"]}]}`, 200,
			[]string{`{"name":"budget","strategy":"fallback","targets":["openai/openai-small","my-deepseek/my-deepseek-large"]}`, `{"name":"Spread","strategy":"round-robin"`}},
		{"PUT", "/api/v1/models/routing", `{"groups":[{"name":"budget","strategy":"fallback","targets":["openai/openai-small"]}]}`, 200,
			[]string{`"groups":[{"name":"budget"`}},
		{"PUT", "/api/v1/models/routing", `{"groups":[{"name":"x","strategy":"random","targets":["a/b"]}]}`, 400, nil},
		{"PUT", "/api/v1/models/routing", `{"groups":[{"name":"x","strategy":"fallback","targets":["nope"]}]}`, 400, nil},
		{"GET", "/api/v1/models/routing", "", 200, []string{`"groups":[{"name":"budget","strategy":"fallback","targets":["openai/openai-small"]}]`}},
		{"GET", "/api/v1/models/bindings", "", 200, []string{`{"harness":"claude-code","model":"","configPath":"/home/agent/.claude/settings.json","updatedAt":"0001-01-01T00:00:00Z"}`,
			`"harness":"opencode"`}},
		{"PUT", "/api/v1/models/bindings/codex", `{"model":"deepseek/deepseek-v4-pro"}`, 200,
			[]string{`"harness":"codex"`, `"model":"deepseek/deepseek-v4-pro"`, `"configPath":"/home/agent/.codex/config.toml"`}},
		{"PUT", "/api/v1/models/bindings/vim", `{"model":"a/b"}`, 400, nil},
		{"PUT", "/api/v1/models/bindings/codex", `{"model":"not a model"}`, 400, nil},
		{"PUT", "/api/v1/models/bindings/codex", `{}`, 400, nil},
		{"DELETE", "/api/v1/models/providers/openai", "", 204, nil},
		{"DELETE", "/api/v1/models/providers/openai", "", 404, nil},
	}
	for _, s := range steps {
		code, body := call(t, srv, s.method, s.path, s.body)
		if code != s.code {
			t.Fatalf("%s %s = %d %s, want %d", s.method, s.path, code, body, s.code)
		}
		for _, c := range s.contains {
			if !strings.Contains(body, c) {
				t.Errorf("%s %s: %s lacks %s", s.method, s.path, body, c)
			}
		}
		if strings.Contains(body, "sk-secret") {
			t.Fatalf("%s %s leaked an API key: %s", s.method, s.path, body)
		}
	}
	if fake.Key("my-deepseek") != "sk-secret-2" {
		t.Fatal("API key not stored in magpie")
	}
	cfg, err := os.ReadFile(filepath.Join(home, ".codex/config.toml"))
	if err != nil || !strings.Contains(string(cfg), `model = "deepseek/deepseek-v4-pro"`) {
		t.Fatalf("binding did not rewrite the codex config: %s %v", cfg, err)
	}
	code, body := call(t, srv, "GET", "/api/v1/models/bindings", "")
	var bs struct {
		Bindings []magpie.Binding `json:"bindings"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &bs) != nil || len(bs.Bindings) != 5 || bs.Bindings[1].Model != "deepseek/deepseek-v4-pro" ||
		bs.Bindings[1].UpdatedAt.IsZero() {
		t.Fatalf("bindings = %s", body)
	}
	// Unbinding keeps the config model and clears the stored binding.
	if code, body := call(t, srv, "PUT", "/api/v1/models/bindings/codex", `{"model":""}`); code != 200 || !strings.Contains(body, `"model":""`) {
		t.Fatalf("unbind = %d %s", code, body)
	}

	fake.SetDown(true)
	for _, p := range []string{"/api/v1/models/providers", "/api/v1/models", "/api/v1/models/routing"} {
		if code, body := call(t, srv, "GET", p, ""); code != 503 || !strings.Contains(body, `"unavailable"`) {
			t.Errorf("%s with magpie down = %d %s", p, code, body)
		}
	}
	if code, _ := call(t, srv, "GET", "/api/v1/models/bindings", ""); code != 200 {
		t.Error("bindings must work while magpie is down")
	}
}
