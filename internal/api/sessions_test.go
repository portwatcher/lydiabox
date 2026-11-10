package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

type sessionsEnv struct {
	srv    *httptest.Server
	runner *harnesstest.Runner
	ws     string
}

type noGit struct{}

func (noGit) Inspect(context.Context, string) (string, string, error) { return "", "", nil }

func newSessionsEnv(t *testing.T) *sessionsEnv {
	t.Helper()
	home := adapterstest.Home(t)
	ws := filepath.Join(filepath.Dir(filepath.Dir(home)), "workspace")
	if err := os.MkdirAll(filepath.Join(ws, "lydiabox"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &harnesstest.Runner{Replay: []byte("replayed\r\n")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Dirs: harnesstest.Dirs{Home: home, Workspace: ws},
		Store: &harnesstest.Store{}, Runner: runner, Waker: &harnesstest.Waker{}, Logger: log,
		KillGrace: 100 * time.Millisecond, TermGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	auth := api.NewAuthenticator(token)
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default", Logger: log},
		api.SessionsRoutes{Manager: mgr, Bridge: &harness.Bridge{Manager: mgr, Git: noGit{}, Logger: log}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &sessionsEnv{srv: srv, runner: runner, ws: ws}
}

func (e *sessionsEnv) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func (e *sessionsEnv) dial(t *testing.T, ctx context.Context, id string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/sessions/" + url.PathEscape(id) + "/ws"
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{Subprotocols: []string{api.PTYSubprotocol},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func readJSON(t *testing.T, ctx context.Context, c *websocket.Conn) map[string]any {
	t.Helper()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.MessageText {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
}

// readUntil reads frames until a binary frame contains want or a control
// message of type want arrives.
func readUntil(t *testing.T, ctx context.Context, c *websocket.Conn, want string) map[string]any {
	t.Helper()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		if typ == websocket.MessageBinary && strings.Contains(string(data), want) {
			return nil
		}
		if typ == websocket.MessageText {
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			if m["type"] == want {
				return m
			}
		}
	}
}

func TestSessionsHTTP(t *testing.T) {
	e := newSessionsEnv(t)
	resp, body := e.do(t, http.MethodGet, "/api/v1/sessions?harness=codex&workspace=/workspace/lydiabox", nil)
	var list struct{ Sessions []harness.Session }
	if resp.StatusCode != 200 || json.Unmarshal(body, &list) != nil || len(list.Sessions) != 1 ||
		list.Sessions[0].ID != "codex:"+adapterstest.CodexID || list.Sessions[0].Usage.OutputTokens != 20311 {
		t.Fatalf("list = %d %s", resp.StatusCode, body)
	}
	for _, tc := range []struct {
		method, path string
		body         any
		code         int
	}{
		{http.MethodGet, "/api/v1/sessions?harness=vim", nil, 400},
		{http.MethodGet, "/api/v1/sessions?workspace=/etc", nil, 400},
		{http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "vim"}, 400},
		{http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "codex", "workspaceDir": "/tmp"}, 400},
		{http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "codex", "bogus": 1}, 400},
		{http.MethodDelete, "/api/v1/sessions/codex:nope", nil, 404},
		{http.MethodDelete, "/api/v1/sessions/" + url.PathEscape("codex:"+adapterstest.CodexID) + "?purge=maybe", nil, 400},
		{http.MethodPost, "/api/v1/sessions/switch", map[string]any{"sourceSessionId": "codex:" + adapterstest.CodexID, "targetHarness": "codex"}, 400},
		{http.MethodPost, "/api/v1/sessions/switch", map[string]any{"sourceSessionId": "codex:missing", "targetHarness": "dsh"}, 404},
	} {
		if resp, body := e.do(t, tc.method, tc.path, tc.body); resp.StatusCode != tc.code {
			t.Errorf("%s %s = %d %s, want %d", tc.method, tc.path, resp.StatusCode, body, tc.code)
		}
	}
	resp, body = e.do(t, http.MethodPost, "/api/v1/sessions/switch",
		map[string]any{"sourceSessionId": "dsh:" + adapterstest.DSHID, "targetHarness": "opencode", "dryRun": true})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"dryRun":true`) || !strings.Contains(string(body), `"handoffMarkdown":"# Handoff`) ||
		!strings.Contains(string(body), `"originalGoal":"Add retry`) {
		t.Fatalf("dry run = %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(t, http.MethodDelete, "/api/v1/sessions/"+url.PathEscape("dsh:"+adapterstest.DSHID)+"?purge=true", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("purge = %d", resp.StatusCode)
	}
}

func TestSessionsWebSocketRoom(t *testing.T) {
	e := newSessionsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, body := e.do(t, http.MethodPost, "/api/v1/sessions", map[string]any{"harness": "claude-code",
		"workspaceDir": adapterstest.Workspace, "resumeNativeId": adapterstest.ClaudeID, "cols": 100, "rows": 30})
	var s harness.Session
	if resp.StatusCode != 201 || json.Unmarshal(body, &s) != nil || !s.Live {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	pty := e.runner.Last()

	a := e.dial(t, ctx, s.ID)
	defer a.CloseNow()
	hello := readJSON(t, ctx, a)
	if hello["type"] != "hello" || hello["sessionId"] != s.ID || hello["replayBytes"] != float64(len("replayed\r\n")) || hello["cols"] != float64(100) {
		t.Fatalf("hello = %v", hello)
	}
	if typ, data, err := a.Read(ctx); err != nil || typ != websocket.MessageBinary || string(data) != "replayed\r\n" {
		t.Fatalf("replay = %v %q %v", typ, data, err)
	}
	b := e.dial(t, ctx, s.ID)
	defer b.CloseNow()
	readJSON(t, ctx, b) // hello

	// Output reaches every client; input from any client reaches the PTY.
	pty.Emit([]byte("compiling...\r\n"))
	readUntil(t, ctx, a, "compiling")
	readUntil(t, ctx, b, "compiling")
	if err := b.Write(ctx, websocket.MessageBinary, []byte("y\r")); err != nil {
		t.Fatal(err)
	}
	ctl := func(c *websocket.Conn, v any) {
		data, _ := json.Marshal(v)
		if err := c.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
	}
	ctl(a, map[string]any{"type": "ping", "id": 7})
	if m := readUntil(t, ctx, a, "pong"); m["id"] != float64(7) {
		t.Fatalf("pong = %v", m)
	}
	ctl(a, map[string]any{"type": "resize", "cols": 160, "rows": 48})
	readUntil(t, ctx, a, "resized")
	if m := readUntil(t, ctx, b, "resized"); m["cols"] != float64(160) {
		t.Fatalf("resized = %v", m)
	}
	ctl(a, map[string]any{"type": "signal", "signal": "SIGKILL"})
	if m := readUntil(t, ctx, a, "error"); m["code"] != "bad_request" {
		t.Fatalf("bad signal = %v", m)
	}
	if got := string(pty.Input()); got != "y\r" {
		t.Fatalf("pty input = %q", got)
	}
	if sz := pty.Sizes(); len(sz) != 1 || sz[0] != [2]uint16{160, 48} {
		t.Fatalf("sizes = %v", sz)
	}

	// Hot-switch: both clients see `switched` and the new session output.
	resp, body = e.do(t, http.MethodPost, "/api/v1/sessions/switch",
		map[string]any{"sourceSessionId": s.ID, "targetHarness": "dsh"})
	var res harness.SwitchResult
	if resp.StatusCode != 200 || json.Unmarshal(body, &res) != nil || res.Session == nil {
		t.Fatalf("switch = %d %s", resp.StatusCode, body)
	}
	for _, c := range []*websocket.Conn{a, b} {
		m := readUntil(t, ctx, c, "switched")
		if m["fromSessionId"] != s.ID || m["toSessionId"] != res.Session.ID || m["handoffPath"] != "/workspace/lydiabox/.agent/HANDOFF.md" {
			t.Fatalf("switched = %v", m)
		}
	}
	target := e.runner.Last()
	target.Emit([]byte("dsh ready"))
	readUntil(t, ctx, a, "dsh ready")
	readUntil(t, ctx, b, "dsh ready")
	if _, err := os.Stat(filepath.Join(e.ws, "lydiabox/.agent/HANDOFF.md")); err != nil {
		t.Fatal(err)
	}

	// A signal from a client ends the target; clients get exit and 1000.
	ctl(b, map[string]any{"type": "signal", "signal": "SIGINT"})
	if m := readUntil(t, ctx, a, "exit"); m["code"] != float64(130) {
		t.Fatalf("exit = %v", m)
	}
	if _, _, err := a.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close after exit = %v", err)
	}

	// The exited session cannot be attached.
	c := e.dial(t, ctx, res.Session.ID)
	defer c.CloseNow()
	if m := readJSON(t, ctx, c); m["type"] != "error" || m["code"] != "conflict" {
		t.Fatalf("attach to exited = %v", m)
	}
}

func TestSessionsWebSocketRequiresSubprotocol(t *testing.T) {
	e := newSessionsEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/sessions/" + url.PathEscape("codex:"+adapterstest.CodexID) + "/ws"
	c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if _, _, err := c.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close = %v", err)
	}
	// Unauthenticated upgrades are rejected before the handshake.
	if _, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{Subprotocols: []string{api.PTYSubprotocol}}); err == nil || resp.StatusCode != 401 {
		t.Fatalf("unauthenticated dial: %v", err)
	}
}
