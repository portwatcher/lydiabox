package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/browser"
	"github.com/lydiabox/lydiabox/internal/guest/browser/cdptest"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

var browserToolNames = []string{"browser_open", "browser_back", "browser_forward", "browser_tabs", "browser_snapshot",
	"browser_click", "browser_type", "browser_select", "browser_press", "browser_scroll", "browser_text", "browser_eval",
	"browser_screenshot", "browser_wait"}

func setupBrowser(t *testing.T) (*httptest.Server, *x11.Controller, *cdptest.Server) {
	t.Helper()
	fake := x11.NewFake(1920, 1080)
	ctl := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return fake, nil }, TakeoverWait: 20 * time.Millisecond})
	cdp := cdptest.New(t)
	cdp.AddTarget("https://example.com/", "Example Domain", true)
	br := browser.New(browser.Options{Endpoint: func(context.Context) (string, error) { return cdp.URL(), nil },
		Desktop: ctl, Poll: 5 * time.Millisecond, SettleTimeout: 100 * time.Millisecond})
	t.Cleanup(br.Close)
	srv := httptest.NewServer(NewServer(ctl, &fakeApps{}, WithBrowser(br)))
	t.Cleanup(srv.Close)
	return srv, ctl, cdp
}

func resultText(res map[string]any) string {
	var parts []string
	for _, c := range res["content"].([]any) {
		if s, ok := c.(map[string]any)["text"].(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func TestBrowserToolsList(t *testing.T) {
	srv, _, _ := setupBrowser(t)
	_, out := post(t, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil)
	tools := out["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		m := tl.(map[string]any)
		names = append(names, m["name"].(string))
		if m["description"] == "" || m["inputSchema"].(map[string]any)["type"] != "object" {
			t.Errorf("tool %v has no description or schema", m["name"])
		}
	}
	got := strings.Join(names, ",")
	if !strings.HasSuffix(got, ",blender_run_script,"+strings.Join(browserToolNames, ",")) || len(names) != 9+len(browserToolNames) {
		t.Fatalf("tools %s", got)
	}
	_, out = post(t, srv.URL, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{}}`, nil)
	if !strings.Contains(out["result"].(map[string]any)["instructions"].(string), "browser_snapshot") {
		t.Error("initialize instructions do not mention the browser tools")
	}
}

func TestBrowserToolsCall(t *testing.T) {
	srv, ctl, cdp := setupBrowser(t)
	events, cancel := ctl.Events().Subscribe()
	defer cancel()

	if txt := resultText(call(t, srv.URL, "browser_open", `{"url":"example.org"}`)); !strings.HasPrefix(txt, "Opened https://example.org") {
		t.Errorf("open: %s", txt)
	}
	if txt := resultText(call(t, srv.URL, "browser_snapshot", `{}`)); !strings.Contains(txt, `[e1] link "Learn more"`) {
		t.Errorf("snapshot: %s", txt)
	}
	if txt := resultText(call(t, srv.URL, "browser_click", `{"ref":"e1"}`)); !strings.HasPrefix(txt, `Clicked e1 link "Learn more"`) {
		t.Errorf("click: %s", txt)
	}
	if n := len(cdp.Called("Input.dispatchMouseEvent")); n != 3 {
		t.Errorf("%d mouse events", n)
	}
	call(t, srv.URL, "browser_type", `{"ref":"e1","text":"hi","submit":true}`)
	call(t, srv.URL, "browser_select", `{"ref":"e1","values":["Blue"]}`)
	call(t, srv.URL, "browser_press", `{"key":"Escape"}`)
	call(t, srv.URL, "browser_scroll", `{"dy":300}`)
	call(t, srv.URL, "browser_wait", `{"for":"load"}`)
	if txt := resultText(call(t, srv.URL, "browser_tabs", `{}`)); !strings.HasPrefix(txt, "* 1  Example Domain") {
		t.Errorf("tabs: %s", txt)
	}
	call(t, srv.URL, "browser_tabs", `{"action":"select","tab":1}`)
	if txt := resultText(call(t, srv.URL, "browser_text", `{"max_chars":7}`)); !strings.HasPrefix(txt, "Example") {
		t.Errorf("text: %s", txt)
	}
	if txt := resultText(call(t, srv.URL, "browser_eval", `{"expression":"location.href"}`)); !strings.Contains(txt, `"expr": "location.href"`) {
		t.Errorf("eval: %s", txt)
	}
	res := call(t, srv.URL, "browser_screenshot", `{"full_page":true}`)
	img := res["content"].([]any)[0].(map[string]any)
	if data, err := base64.StdEncoding.DecodeString(img["data"].(string)); err != nil || img["mimeType"] != "image/png" || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Errorf("screenshot %v %v", img["mimeType"], err)
	}
	call(t, srv.URL, "browser_open", `{"url":"https://b.example/","new_tab":true}`)
	call(t, srv.URL, "browser_back", `{}`)
	call(t, srv.URL, "browser_tabs", `{"action":"close","tab":2}`)

	var kinds []string
	for len(events) > 0 {
		var ev x11.AgentAction
		_ = json.Unmarshal(<-events, &ev)
		kinds = append(kinds, ev.Action+":"+ev.Tool)
	}
	got := strings.Join(kinds, ",")
	for _, want := range []string{"launch:browser_open", "screenshot:browser_snapshot", "click:browser_click", "type:browser_type",
		"key:browser_type", "click:browser_select", "key:browser_press", "scroll:browser_scroll", "screenshot:browser_screenshot"} {
		if !strings.Contains(got, want) {
			t.Errorf("event %s missing in %s", want, got)
		}
	}

	for tool, args := range map[string]string{
		"browser_click": `{"ref":"e404"}`, "browser_open": `{}`, "browser_tabs": `{"action":"select"}`,
		"browser_wait": `{"for":"selector"}`, "browser_snapshot": `{"bogus":1}`,
	} {
		if res := call(t, srv.URL, tool, args); res["isError"] != true {
			t.Errorf("%s %s: no error: %v", tool, args, res)
		}
	}

	if _, err := ctl.SetMode(rpc.ModeTakeover, "user"); err != nil {
		t.Fatal(err)
	}
	res = call(t, srv.URL, "browser_click", `{"ref":"e1"}`)
	if res["isError"] != true || resultText(res) != x11.TakeoverMessage {
		t.Fatalf("takeover result %v", res)
	}
	if res := call(t, srv.URL, "browser_snapshot", `{}`); res["isError"] == true {
		t.Fatalf("snapshot during takeover: %v", res)
	}
}
