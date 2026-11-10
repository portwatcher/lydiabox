package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

type fakeApps struct {
	mu       sync.Mutex
	launched []string
}

func (f *fakeApps) Launch(_ context.Context, app string) error {
	if app != "chrome" && app != "blender" && app != "terminal" {
		return errors.New("app must be chrome, blender, or terminal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launched = append(f.launched, app)
	return nil
}

func (f *fakeApps) CDPEndpoint(context.Context) (string, error) {
	return "ws://127.0.0.1:9222/devtools/browser/abc", nil
}

func (f *fakeApps) RunBlender(_ context.Context, req BlenderRequest) (BlenderResult, error) {
	return BlenderResult{Output: "ran " + req.Code, ExitCode: 0}, nil
}

func setup(t *testing.T) (*httptest.Server, *x11.Controller, *x11.Fake) {
	t.Helper()
	fake := x11.NewFake(1920, 1080)
	ctl := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return fake, nil }, TakeoverWait: 20 * time.Millisecond})
	srv := httptest.NewServer(NewServer(ctl, &fakeApps{}))
	t.Cleanup(srv.Close)
	return srv, ctl, fake
}

func post(t *testing.T, url, body string, hdr map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func call(t *testing.T, url, tool, args string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, args)
	resp, out := post(t, url, body, nil)
	if resp.StatusCode != 200 || out["error"] != nil {
		t.Fatalf("%s: status %d %v", tool, resp.StatusCode, out)
	}
	return out["result"].(map[string]any)
}

func TestProtocolHandshakeAndToolList(t *testing.T) {
	srv, _, _ := setup(t)
	resp, out := post(t, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" || res["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatalf("initialize %v", res)
	}
	if id, _ := out["id"].(float64); id != 1 {
		t.Fatalf("id %v", out["id"])
	}

	resp, _ = post(t, srv.URL, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("notification status %d", resp.StatusCode)
	}

	_, out = post(t, srv.URL, `{"jsonrpc":"2.0","id":"x","method":"tools/list"}`, nil)
	tools := out["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	want := "computer_screenshot,computer_mouse_click,computer_mouse_move,computer_mouse_drag,computer_scroll," +
		"computer_keyboard,desktop_launch_app,browser_cdp_endpoint,blender_run_script"
	if strings.Join(names, ",") != want {
		t.Fatalf("tools %v", names)
	}

	_, out = post(t, srv.URL, `{"jsonrpc":"2.0","id":2,"method":"nope"}`, nil)
	if out["error"].(map[string]any)["code"].(float64) != codeMethodNotFound {
		t.Fatalf("unknown method %v", out)
	}
	_, out = post(t, srv.URL, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"missing"}}`, nil)
	if out["error"].(map[string]any)["code"].(float64) != codeInvalidParams {
		t.Fatalf("unknown tool %v", out)
	}

	resp, _ = post(t, srv.URL, `{"jsonrpc":"2.0","id":4,"method":"ping"}`, map[string]string{"Origin": "https://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"}]`))
	req.Header.Set("Content-Type", "application/json")
	bresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var batch []map[string]any
	_ = json.NewDecoder(bresp.Body).Decode(&batch)
	bresp.Body.Close()
	if len(batch) != 1 {
		t.Fatalf("batch %v", batch)
	}
}

func TestToolsDriveTheDesktop(t *testing.T) {
	srv, ctl, fake := setup(t)
	res := call(t, srv.URL, "computer_screenshot", `{}`)
	content := res["content"].([]any)
	img := content[0].(map[string]any)
	data, err := base64.StdEncoding.DecodeString(img["data"].(string))
	if err != nil || img["mimeType"] != "image/png" || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("screenshot %v %v", img["mimeType"], err)
	}
	res = call(t, srv.URL, "computer_screenshot", `{"format":"jpeg","quality":50}`)
	if res["content"].([]any)[0].(map[string]any)["mimeType"] != "image/jpeg" {
		t.Fatal("jpeg screenshot")
	}

	fake.Reset()
	call(t, srv.URL, "computer_mouse_click", `{"x":100,"y":200,"button":"right"}`)
	call(t, srv.URL, "computer_mouse_drag", `{"from_x":1,"from_y":2,"x":11,"y":12}`)
	call(t, srv.URL, "computer_scroll", `{"x":5,"y":5,"dy":-2}`)
	call(t, srv.URL, "computer_keyboard", `{"text":"hi","key_combo":"Return"}`)
	ev := strings.Join(fake.Events(), "|")
	for _, w := range []string{"move 100 200|button 3 down|button 3 up", "move 1 2|button 1 down", "move 11 12|button 1 up",
		"button 4 down", "key 0x68 down", "key 0xff0d down"} {
		if !strings.Contains(ev, w) {
			t.Fatalf("missing %q in %s", w, ev)
		}
	}

	res = call(t, srv.URL, "computer_mouse_click", `{"x":1}`)
	if res["isError"] != true {
		t.Fatalf("missing y accepted: %v", res)
	}
	res = call(t, srv.URL, "computer_mouse_move", `{"x":1,"y":2,"bogus":1}`)
	if res["isError"] != true {
		t.Fatalf("unknown argument accepted: %v", res)
	}
	res = call(t, srv.URL, "desktop_launch_app", `{"app":"chrome"}`)
	if res["isError"] == true {
		t.Fatalf("launch %v", res)
	}
	res = call(t, srv.URL, "browser_cdp_endpoint", `{}`)
	if !strings.HasPrefix(res["content"].([]any)[0].(map[string]any)["text"].(string), "ws://127.0.0.1:9222/") {
		t.Fatalf("cdp %v", res)
	}
	res = call(t, srv.URL, "blender_run_script", `{"code":"import bpy"}`)
	if res["content"].([]any)[0].(map[string]any)["text"] != "ran import bpy" {
		t.Fatalf("blender %v", res)
	}

	if _, err := ctl.SetMode(rpc.ModeTakeover, "user"); err != nil {
		t.Fatal(err)
	}
	res = call(t, srv.URL, "computer_mouse_click", `{"x":1,"y":1}`)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	if res["isError"] != true || text != x11.TakeoverMessage {
		t.Fatalf("takeover result %v", res)
	}
}

func TestLocalAppsStartsChromeForCDP(t *testing.T) {
	var (
		mu      sync.Mutex
		started [][]string
		up      bool
	)
	cdp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up || r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"webSocketDebuggerUrl":"ws://127.0.0.1/devtools/browser/x"}`)
	}))
	defer cdp.Close()
	_, portStr, _ := net.SplitHostPort(cdp.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)
	apps := NewLocalApps(LocalAppsOptions{
		CDPPort:  port,
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Start: func(path string, args, env []string, dir string) error {
			mu.Lock()
			defer mu.Unlock()
			started = append(started, append([]string{path}, args...))
			up = true
			return nil
		},
	})
	ws, err := apps.CDPEndpoint(context.Background())
	if err != nil || ws != "ws://127.0.0.1/devtools/browser/x" {
		t.Fatalf("CDPEndpoint %q %v", ws, err)
	}
	if len(started) != 1 || started[0][0] != "/usr/bin/google-chrome-stable" ||
		!strings.Contains(strings.Join(started[0], " "), "--user-data-dir=/home/agent/.config/google-chrome") {
		t.Fatalf("started %v", started)
	}
	if _, err := apps.CDPEndpoint(context.Background()); err != nil || len(started) != 1 {
		t.Fatalf("second call restarted chrome: %v %v", started, err)
	}
}
