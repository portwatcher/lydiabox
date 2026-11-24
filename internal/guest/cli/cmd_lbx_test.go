package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/browser"
	"github.com/portwatcher/lydiabox/internal/guest/browser/cdptest"
	"github.com/portwatcher/lydiabox/internal/guest/mcp"
	"github.com/portwatcher/lydiabox/internal/guest/x11"
)

type lbxApps struct{}

func (lbxApps) Launch(context.Context, string) error { return nil }
func (lbxApps) CDPEndpoint(context.Context) (string, error) {
	return "ws://127.0.0.1:9222/devtools/browser/x", nil
}
func (lbxApps) RunBlender(context.Context, mcp.BlenderRequest) (mcp.BlenderResult, error) {
	return mcp.BlenderResult{Output: "ok"}, nil
}

// lbxRig starts a guest MCP server with a fake desktop and a fake Chrome
// and points lbx at it.
func lbxRig(t *testing.T) (*x11.Fake, *cdptest.Server) {
	t.Helper()
	fake := x11.NewFake(1920, 1080)
	ctl := x11.NewController(x11.Options{Dial: func() (x11.Backend, error) { return fake, nil }, TakeoverWait: 20 * time.Millisecond})
	cdp := cdptest.New(t)
	cdp.AddTarget("https://example.com/", "Example Domain", true)
	br := browser.New(browser.Options{Endpoint: func(context.Context) (string, error) { return cdp.URL(), nil },
		Desktop: ctl, Poll: 5 * time.Millisecond, SettleTimeout: 100 * time.Millisecond})
	t.Cleanup(br.Close)
	srv := httptest.NewServer(mcp.NewServer(ctl, lbxApps{}, mcp.WithBrowser(br)))
	t.Cleanup(srv.Close)
	t.Setenv(MCPURLEnv, srv.URL+"/mcp")
	return fake, cdp
}

func runCLI(t *testing.T, argv0 string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(context.Background(), argv0, args, Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb})
	return code, out.String(), errb.String()
}

func TestLbxBrowser(t *testing.T) {
	_, cdp := lbxRig(t)
	code, out, errs := runCLI(t, "/usr/local/bin/lbx", "", "browser", "open", "example.org")
	if code != 0 || !strings.HasPrefix(out, "Opened https://example.org\nTab 1: Example Domain") {
		t.Fatalf("open: %d %q %q", code, out, errs)
	}
	// The same operations through lydiabox-guest.
	code, out, _ = runCLI(t, "lydiabox-guest", "", "browser", "snapshot")
	if code != 0 || !strings.Contains(out, `[e1] link "Learn more" -> https://iana.org/domains/example`) {
		t.Fatalf("snapshot: %d %q", code, out)
	}
	if code, out, _ = runCLI(t, "lbx", "", "browser", "click", "[e1]"); code != 0 || !strings.HasPrefix(out, `Clicked e1 link "Learn more"`) {
		t.Fatalf("click: %d %q", code, out)
	}
	if code, _, _ = runCLI(t, "lbx", "", "browser", "type", "e1", "hello", "world", "--submit"); code != 0 {
		t.Fatal("type failed")
	}
	if ins := cdp.Called("Input.insertText"); len(ins) != 1 || ins[0]["text"] != "hello world" {
		t.Errorf("typed %v", ins)
	}
	runCLI(t, "lbx", "", "browser", "fill", "e1", "x")
	if code, _, _ = runCLI(t, "lbx", "", "browser", "scroll", "up", "300"); code != 0 {
		t.Fatal("scroll failed")
	}
	wheel := cdp.Called("Input.dispatchMouseEvent")
	if w := wheel[len(wheel)-1]; w["deltaY"].(float64) != -300 {
		t.Errorf("scroll up event %v", w)
	}
	if code, out, _ = runCLI(t, "lbx", "if (1) 2", "browser", "eval", "-"); code != 0 || !strings.Contains(out, `"expr": "if (1) 2"`) {
		t.Errorf("eval from stdin: %d %q", code, out)
	}
	shot := filepath.Join(t.TempDir(), "page.png")
	code, out, _ = runCLI(t, "lbx", "", "browser", "screenshot", "--full", "--out", shot)
	if b, err := os.ReadFile(shot); code != 0 || err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) || !strings.HasPrefix(out, shot+"\n") {
		t.Errorf("screenshot: %d %q %v", code, out, err)
	}
	code, out, _ = runCLI(t, "lbx", "", "browser", "tabs", "--json")
	var res struct {
		Content []struct{ Text string } `json:"content"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &res) != nil || !strings.HasPrefix(res.Content[0].Text, "* 1") {
		t.Errorf("tabs --json: %d %q", code, out)
	}
	for _, args := range [][]string{{"browser", "wait", "load"}, {"browser", "tab", "1"}, {"browser", "text", "--max", "5"},
		{"browser", "select", "e1", "Blue"}, {"browser", "press", "Escape", "--ref", "e1"}, {"browser", "back"}} {
		if code, out, errs := runCLI(t, "lbx", "", args...); code != 0 && !strings.Contains(errs, "no page to go back") {
			t.Errorf("%v: %d %q %q", args, code, out, errs)
		}
	}

	// A tool error goes to stderr with exit code 1.
	code, out, errs = runCLI(t, "lbx", "", "browser", "click", "e404")
	if code != 1 || out != "" || !strings.Contains(errs, "take a new browser snapshot") {
		t.Errorf("bad ref: %d %q %q", code, out, errs)
	}
	// Usage errors name the usage.
	code, _, errs = runCLI(t, "lbx", "", "browser", "click")
	if code != 1 || !strings.Contains(errs, "usage: lbx browser click REF") {
		t.Errorf("usage error: %d %q", code, errs)
	}
	if code, _, errs = runCLI(t, "lbx", "", "browser", "open", "x", "--bogus"); code != 1 || !strings.Contains(errs, "unknown flag --bogus") {
		t.Errorf("unknown flag: %d %q", code, errs)
	}
	if code, _, errs = runCLI(t, "lbx", "", "browser", "fly"); code != 1 || !strings.Contains(errs, `unknown operation "fly"`) {
		t.Errorf("unknown op: %d %q", code, errs)
	}
}

func TestLbxComputerToolsAndCall(t *testing.T) {
	fake, _ := lbxRig(t)
	t.Setenv("TMPDIR", t.TempDir())
	code, out, errs := runCLI(t, "lbx", "", "computer", "screenshot")
	path := strings.SplitN(out, "\n", 2)[0]
	if code != 0 || !strings.HasPrefix(path, os.TempDir()) || !strings.HasSuffix(path, ".png") || !strings.Contains(out, "Screenshot 1920x1080") {
		t.Fatalf("screenshot: %d %q %q", code, out, errs)
	}
	if b, err := os.ReadFile(path); err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("screenshot file: %v", err)
	}
	fake.Reset()
	for _, args := range [][]string{
		{"computer", "click", "100", "200", "--button", "right"},
		{"computer", "move", "5", "6"},
		{"computer", "drag", "1", "2", "11", "12"},
		{"computer", "scroll", "5", "5", "up", "2"},
		{"computer", "type", "hi"},
		{"computer", "key", "ctrl+l"},
		{"computer", "launch", "chrome"},
	} {
		if code, out, errs := runCLI(t, "lydiabox-guest", "", args...); code != 0 {
			t.Errorf("%v: %d %q %q", args, code, out, errs)
		}
	}
	ev := strings.Join(fake.Events(), "|")
	for _, w := range []string{"move 100 200|button 3 down|button 3 up", "move 5 6", "move 11 12|button 1 up", "button 4 down", "key 0x68 down"} {
		if !strings.Contains(ev, w) {
			t.Errorf("missing %q in %s", w, ev)
		}
	}
	if code, _, errs = runCLI(t, "lbx", "", "computer", "scroll", "1", "1", "sideways"); code != 1 || !strings.Contains(errs, "direction") {
		t.Errorf("bad direction: %d %q", code, errs)
	}

	code, out, _ = runCLI(t, "lbx", "", "tools")
	if code != 0 || !strings.Contains(out, "browser_snapshot") || !strings.Contains(out, "computer_screenshot") {
		t.Errorf("tools: %d %q", code, out)
	}
	if code, out, _ = runCLI(t, "lbx", "", "call", "blender_run_script", `{"code":"print(1)"}`); code != 0 || out != "ok\n" {
		t.Errorf("call: %d %q", code, out)
	}
	if code, out, _ = runCLI(t, "lbx", `{"code":"x"}`, "call", "blender_run_script", "-"); code != 0 || out != "ok\n" {
		t.Errorf("call stdin: %d %q", code, out)
	}
	if code, _, errs = runCLI(t, "lbx", "", "call", "nope"); code != 1 || !strings.Contains(errs, "unknown tool") {
		t.Errorf("unknown tool: %d %q", code, errs)
	}
	if code, out, _ = runCLI(t, "lbx", ""); code != 1 || !strings.Contains(out, "lbx browser <op>") {
		t.Errorf("bare lbx: %d %q", code, out)
	}
	if code, out, _ = runCLI(t, "lbx", "", "computer", "help"); code != 0 || !strings.Contains(out, "screenshot [--jpeg] [--out FILE]") {
		t.Errorf("computer help: %d %q", code, out)
	}
}

func TestLbxDaemonDown(t *testing.T) {
	t.Setenv(MCPURLEnv, "http://127.0.0.1:1/mcp")
	code, _, errs := runCLI(t, "lbx", "", "browser", "tabs")
	if code != 1 || !strings.Contains(errs, "guest daemon is not reachable") {
		t.Errorf("daemon down: %d %q", code, errs)
	}
}
