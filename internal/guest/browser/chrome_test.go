package browser

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRealChrome runs the page scripts in a real Chrome. Set
// LYDIABOX_TEST_CHROME to the Chrome executable to run it.
func TestRealChrome(t *testing.T) {
	bin := os.Getenv("LYDIABOX_TEST_CHROME")
	if bin == "" {
		t.Skip("LYDIABOX_TEST_CHROME is not set")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/next":
			fmt.Fprintf(w, `<title>Next</title><h1>Second page</h1><p>q=%s color=%s</p><a href="/">Home</a>`,
				r.URL.Query().Get("q"), r.URL.Query().Get("color"))
		default:
			fmt.Fprint(w, testPage)
		}
	}))
	defer site.Close()

	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--headless=new", "--remote-debugging-port=0", "--user-data-dir="+dir,
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var ws string
	for ws == "" {
		if ctx.Err() != nil {
			t.Fatal("chrome did not write DevToolsActivePort")
		}
		if b, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort")); err == nil {
			if l := strings.Split(strings.TrimSpace(string(b)), "\n"); len(l) == 2 {
				ws = "ws://127.0.0.1:" + l[0] + l[1]
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	desk := &fakeDesktop{}
	br := New(Options{Endpoint: func(context.Context) (string, error) { return ws, nil }, Desktop: desk})
	defer br.Close()

	must := func(s string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		t.Log(s)
		return s
	}
	must(br.Open(ctx, site.URL+"/", false))
	snap := must(br.Snapshot(ctx, 0))
	for _, want := range []string{`heading 1 "Test page"`, `[e1] link "Learn more" -> ` + site.URL + "/next", `textbox "Query"`,
		`combobox "Color" value="Red" options: Red | Green | Blue`, `checkbox "Agree" unchecked`, `button "Go"`, `clickable "Div button"`} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot misses %q", want)
		}
	}
	// Refs are stable across snapshots of the same document.
	if again := must(br.Snapshot(ctx, 0)); again != snap {
		t.Errorf("second snapshot differs:\n%s", again)
	}
	ref := func(label string) string {
		for _, l := range strings.Split(snap, "\n") {
			if strings.Contains(l, label) && strings.HasPrefix(l, "[") {
				return l[1:strings.IndexByte(l, ']')]
			}
		}
		t.Fatalf("no ref for %s", label)
		return ""
	}
	must(br.Click(ctx, ref(`clickable "Div button"`), false, ""))
	if got := must(br.Eval(ctx, "document.getElementById('out').textContent")); got != "div clicked" {
		t.Errorf("div click: %q", got)
	}
	must(br.Type(ctx, ref(`textbox "Query"`), "hello", false, false))
	must(br.Type(ctx, ref(`textbox "Query"`), "world", true, false))
	if got := must(br.Eval(ctx, "document.querySelector('#q').value")); got != "world" {
		t.Errorf("typed value %q", got)
	}
	must(br.Select(ctx, ref(`combobox "Color"`), []string{"Blue"}))
	must(br.Click(ctx, ref(`checkbox "Agree"`), false, ""))
	if s := must(br.Snapshot(ctx, 0)); !strings.Contains(s, `checkbox "Agree" checked`) || !strings.Contains(s, `value="Blue"`) {
		t.Errorf("state not in snapshot:\n%s", s)
	}
	must(br.Press(ctx, "ctrl+a", ref(`textbox "Query"`)))
	must(br.Press(ctx, "Backspace", ""))
	must(br.Type(ctx, "", "abc", false, true))
	must(br.Wait(ctx, "text", "Second page", 10*time.Second))
	if txt := must(br.Text(ctx, "", 0)); !strings.Contains(txt, "q=abc color=blue") {
		t.Errorf("form submit text: %q", txt)
	}
	must(br.History(ctx, -1))
	if s := must(br.Snapshot(ctx, 0)); !strings.Contains(s, "[e1] link") {
		t.Errorf("refs did not restart on the new document:\n%s", s)
	}
	must(br.Click(ctx, "e1", false, ""))
	must(br.Wait(ctx, "url", "/next", 10*time.Second))
	must(br.Open(ctx, site.URL+"/?tab=2", true))
	tabs := must(br.Tabs(ctx))
	if !strings.Contains(tabs, "* 2") {
		t.Errorf("new tab is not current:\n%s", tabs)
	}
	must(br.Scroll(ctx, "", 0, 400))
	png, _, err := br.Screenshot(ctx, true)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("screenshot: %v", err)
	}
	must(br.SelectTab(ctx, "1"))
	must(br.CloseTab(ctx, "2"))
	if _, err := br.Click(ctx, "e999", false, ""); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Errorf("stale ref error: %v", err)
	}
	if len(desk.events) == 0 {
		t.Error("no agent_action events")
	}
}

const testPage = `<!doctype html><title>Test</title>
<h1>Test page</h1>
<p>Some text. <a href="/next">Learn more</a></p>
<form action="/next">
<label>Query <input id="q" name="q"></label>
<label for="c">Color</label><select id="c" name="color"><option value="red">Red</option><option value="green">Green</option><option value="blue">Blue</option></select>
<label><input type="checkbox" name="agree"> Agree</label>
<button>Go</button>
</form>
<div style="cursor:pointer" onclick="document.getElementById('out').textContent='div clicked'">Div button</div>
<div id="out"></div>
<div style="height:3000px"></div>
<button style="display:none">Hidden</button>`
