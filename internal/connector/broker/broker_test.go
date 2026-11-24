package broker_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/agentfs"
	"github.com/portwatcher/lydiabox/internal/connector"
	"github.com/portwatcher/lydiabox/internal/connector/broker"
	"github.com/portwatcher/lydiabox/internal/connector/connectortest"
	"github.com/portwatcher/lydiabox/internal/store"
)

const tenantID = "default"

type dirs struct{ home, workspace string }

func (d dirs) HomeDir(string) (string, error)      { return d.home, nil }
func (d dirs) WorkspaceDir(string) (string, error) { return d.workspace, nil }

type env struct {
	mgr    *connector.Manager
	st     *store.Store
	fake   *connectortest.Fake
	brk    *broker.Broker
	srv    *httptest.Server
	dirs   dirs
	tokens map[string]string
}

func newEnv(t *testing.T, connect ...string) *env {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(base, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := connectortest.New(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := connector.New(connector.Options{Store: st, Providers: fake.Providers(), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	d := dirs{home: filepath.Join(base, "home"), workspace: filepath.Join(base, "workspace")}
	for _, p := range []string{d.home, d.workspace, filepath.Join(base, "outside")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	brk, err := broker.New(broker.Options{Manager: mgr, TenantID: tenantID, Files: agentfs.NewLocal(d, agentfs.LocalOptions{}), Logger: log, MaxDownloadBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{mgr: mgr, st: st, fake: fake, brk: brk, dirs: d, tokens: map[string]string{}}
	for _, p := range connect {
		tok := fake.IssueToken(p, "me")
		if _, err := mgr.BindToken(context.Background(), tenantID, p, connector.BindTokenRequest{Token: tok}); err != nil {
			t.Fatal(err)
		}
		e.tokens[p] = tok
	}
	e.srv = httptest.NewServer(brk)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) post(t *testing.T, path string, body any) (int, []byte) {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

type rpcResult struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *env) rpc(t *testing.T, method string, params any) rpcResult {
	t.Helper()
	code, body := e.post(t, "/mcp", map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	if code != http.StatusOK {
		t.Fatalf("%s: HTTP %d %s", method, code, body)
	}
	var r rpcResult
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

type toolOut struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// callTool returns the text result of a tool and whether it is an error.
func (e *env) callTool(t *testing.T, name string, args map[string]any) (string, bool) {
	t.Helper()
	r := e.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})
	if r.Error != nil {
		t.Fatalf("%s: rpc error %+v", name, r.Error)
	}
	var out toolOut
	if err := json.Unmarshal(r.Result, &out); err != nil || len(out.Content) != 1 || out.Content[0].Type != "text" {
		t.Fatalf("%s: result %s", name, r.Result)
	}
	return out.Content[0].Text, out.IsError
}

func (e *env) mustTool(t *testing.T, name string, args map[string]any, want ...string) string {
	t.Helper()
	text, isErr := e.callTool(t, name, args)
	if isErr {
		t.Fatalf("%s failed: %s", name, text)
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("%s result lacks %q:\n%s", name, w, text)
		}
	}
	return text
}

func (e *env) audit(t *testing.T, provider string) []store.AuditEntry {
	t.Helper()
	page, err := e.mgr.Audit(context.Background(), tenantID, provider, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return page.Entries
}

func TestGitCred(t *testing.T) {
	e := newEnv(t, connector.GitHub)
	code, body := e.post(t, "/git-cred", broker.GitCredRequest{Protocol: "https", Host: "github.com"})
	var cred broker.GitCredResponse
	if code != http.StatusOK || json.Unmarshal(body, &cred) != nil || cred.Username != "x-access-token" || cred.Password != e.tokens[connector.GitHub] {
		t.Fatalf("git-cred: %d %s", code, body)
	}
	for _, req := range []broker.GitCredRequest{{Protocol: "https", Host: "gitlab.com"}, {Protocol: "http", Host: "github.com"}} {
		if code, body := e.post(t, "/git-cred", req); code != http.StatusNotFound {
			t.Fatalf("%+v: %d %s", req, code, body)
		}
	}
	if code, _ := e.post(t, "/git-cred", map[string]any{"protocol": "https", "host": "github.com", "bogus": 1}); code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", code)
	}
	entries := e.audit(t, connector.GitHub)
	if entries[0].Kind != connector.KindTokenIssued ||
		entries[0].Detail != `{"account":"me","consumer":"git-cred","host":"github.com","provider":"github"}` {
		t.Fatalf("audit = %+v", entries[0])
	}
}

func TestMCPProtocol(t *testing.T) {
	e := newEnv(t)
	r := e.rpc(t, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}})
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(r.Result, &init); err != nil || init.ProtocolVersion != "2025-03-26" ||
		init.ServerInfo.Name != "lydiabox-connectors" || init.Capabilities["tools"] == nil {
		t.Fatalf("initialize = %s", r.Result)
	}
	code, body := e.post(t, "/mcp", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if code != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("notification: %d %q", code, body)
	}
	r = e.rpc(t, "tools/list", nil)
	var list struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(r.Result, &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range list.Tools {
		names[tl.Name] = true
		if tl.InputSchema["type"] != "object" {
			t.Fatalf("%s schema = %v", tl.Name, tl.InputSchema)
		}
	}
	for _, want := range []string{"github_list_repos", "github_list_issues", "github_create_issue", "github_list_pull_requests",
		"github_create_pull_request", "github_get_file_contents", "gdrive_search", "gdrive_download_to_workspace",
		"gdrive_upload_from_workspace", "notion_search", "notion_read_page", "notion_append_to_page",
		"slack_list_channels", "slack_post_message", "slack_search_messages"} {
		if !names[want] {
			t.Fatalf("tools/list lacks %s", want)
		}
	}
	if r := e.rpc(t, "resources/list", nil); r.Error == nil || r.Error.Code != -32601 {
		t.Fatalf("unknown method: %+v", r.Error)
	}
	if r := e.rpc(t, "tools/call", map[string]any{"name": "nope"}); r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("unknown tool: %+v", r.Error)
	}
	resp, err := http.Post(e.srv.URL+"/mcp", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatal(err)
	}
	var pe rpcResult
	_ = json.NewDecoder(resp.Body).Decode(&pe)
	resp.Body.Close()
	if pe.Error == nil || pe.Error.Code != -32700 {
		t.Fatalf("parse error: %+v", pe.Error)
	}
	// A tool of a provider that is not connected fails as a tool error and is audited.
	text, isErr := e.callTool(t, "slack_list_channels", nil)
	if !isErr || !strings.Contains(text, "not connected") {
		t.Fatalf("not connected: %v %s", isErr, text)
	}
	if a := e.audit(t, connector.Slack); len(a) != 1 || a[0].Kind != connector.KindMCPCall || !strings.Contains(a[0].Detail, `"ok":false`) {
		t.Fatalf("audit = %+v", a)
	}
}

func TestServeOnUnixSocket(t *testing.T) {
	e := newEnv(t, connector.GitHub)
	dir, err := os.MkdirTemp("", "lbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.brk.Serve(ctx, ln) }()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := client.Post("http://host/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"result":{}`) {
		t.Fatalf("ping = %s", b)
	}
	client.CloseIdleConnections()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}
