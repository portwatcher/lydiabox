package browser

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/browser/cdptest"
	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

func newTest(t *testing.T) (*Browser, *cdptest.Server, *fakeDesktop) {
	t.Helper()
	f := cdptest.New(t)
	d := &fakeDesktop{}
	b := New(Options{Endpoint: func(context.Context) (string, error) { return f.URL(), nil }, Desktop: d,
		Poll: 5 * time.Millisecond, LoadTimeout: time.Second, SettleTimeout: 200 * time.Millisecond})
	t.Cleanup(b.Close)
	return b, f, d
}

// ok returns a checker of (string, error) results.
func ok(t *testing.T) func(string, error) string {
	return func(s string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func TestOpenNavigatesCurrentTab(t *testing.T) {
	b, f, d := newTest(t)
	f.AddTarget("about:blank", "", false)
	vis := f.AddTarget("https://old.example/", "Old", true)
	ctx := context.Background()
	out := ok(t)(b.Open(ctx, "example.com", false))
	if !strings.Contains(out, "Opened https://example.com") || !strings.Contains(out, "Tab 2") {
		t.Errorf("open output:\n%s", out)
	}
	if f.URLOf(vis) != "https://example.com" {
		t.Errorf("visible tab URL %q; the navigation went to the wrong tab", f.URLOf(vis))
	}
	if got := d.actions(); !slices.Equal(got, []string{"launch:browser_open"}) {
		t.Errorf("events %v", got)
	}
	f.SetNavError("net::ERR_NAME_NOT_RESOLVED")
	if _, err := b.Open(ctx, "https://nx.invalid", false); err == nil || !strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
		t.Errorf("navigation error: %v", err)
	}
}

func TestNewTabSelectCloseAndList(t *testing.T) {
	b, f, _ := newTest(t)
	f.AddTarget("https://a.example/", "A", true)
	ctx := context.Background()
	ok(t)(b.Open(ctx, "https://b.example/", true))
	list := ok(t)(b.Tabs(ctx))
	if list != "  1  A  https://a.example/\n* 2  New  https://b.example/" {
		t.Errorf("tab list:\n%s", list)
	}
	ok(t)(b.SelectTab(ctx, "1"))
	if list := ok(t)(b.Tabs(ctx)); !strings.HasPrefix(list, "* 1") {
		t.Errorf("after select:\n%s", list)
	}
	if _, err := b.SelectTab(ctx, "9"); err == nil || !strings.Contains(err.Error(), "no tab 9") {
		t.Errorf("unknown tab: %v", err)
	}
	if _, err := b.SelectTab(ctx, "x"); err == nil {
		t.Error("invalid tab accepted")
	}
	out := ok(t)(b.CloseTab(ctx, "2"))
	if !strings.Contains(out, "Closed tab 2") || strings.Contains(out, "b.example/\n") {
		t.Errorf("close output:\n%s", out)
	}
	// Tab numbers stay: a new tab gets 3, not 2.
	ok(t)(b.Open(ctx, "https://c.example/", true))
	if list := ok(t)(b.Tabs(ctx)); !strings.Contains(list, "* 3  New  https://c.example/") {
		t.Errorf("tab numbers:\n%s", list)
	}
}

func TestCurrentFollowsVisibleTabAndCreatesOne(t *testing.T) {
	b, f, _ := newTest(t)
	ctx := context.Background()
	// No tab at all: a blank tab is created.
	ok(t)(b.Snapshot(ctx, 0))
	if len(f.Called("Target.createTarget")) != 1 {
		t.Fatal("no tab was created for a browser without tabs")
	}
	t2 := f.AddTarget("https://two.example/", "Two", false)
	ok(t)(b.Snapshot(ctx, 0))
	// The user switches to tab 2 during a takeover.
	f.Lock(func(ts []*cdptest.Target) { ts[0].Visible, t2.Visible = false, true })
	if out := ok(t)(b.Snapshot(ctx, 0)); !strings.HasPrefix(out, "Tab 2: Two") {
		t.Errorf("current tab did not follow the visible tab:\n%s", out)
	}
}

func TestSnapshotFormat(t *testing.T) {
	b, f, d := newTest(t)
	f.AddTarget("https://example.com/", "Example Domain", true)
	out := ok(t)(b.Snapshot(context.Background(), 0))
	want := "Tab 1: Example Domain\nURL: https://example.com/\nScroll: y=0 of 900, viewport height 800\n" +
		"heading 1 \"Example Domain\"\n[e1] link \"Learn more\" -> https://iana.org/domains/example"
	if out != want {
		t.Errorf("snapshot:\n%s\nwant:\n%s", out, want)
	}
	if got := d.actions(); !slices.Equal(got, []string{"screenshot:browser_snapshot"}) {
		t.Errorf("events %v", got)
	}
	f.SetEval(func(_ *cdptest.Target, name, _ string) any {
		if name == "snapshot" {
			return map[string]any{"title": "", "url": "about:blank", "lines": []string{}, "truncated": true, "scroll": []int{0, 0, 0}}
		}
		return nil
	})
	if out := ok(t)(b.Snapshot(context.Background(), 7)); !strings.Contains(out, "no headings") || !strings.Contains(out, "stopped after 7 lines") {
		t.Errorf("empty snapshot:\n%s", out)
	}
}

func TestClickDispatchesMouseEvents(t *testing.T) {
	b, f, d := newTest(t)
	f.AddTarget("https://example.com/", "Example Domain", true)
	ctx := context.Background()
	out := ok(t)(b.Click(ctx, "e1", true, ""))
	if !strings.HasPrefix(out, `Clicked e1 link "Learn more"`) {
		t.Errorf("click output:\n%s", out)
	}
	ev := f.Called("Input.dispatchMouseEvent")
	var types []string
	for _, e := range ev {
		types = append(types, e["type"].(string))
		if e["x"].(float64) != 100.5 || e["y"].(float64) != 200 {
			t.Errorf("mouse event at %v,%v", e["x"], e["y"])
		}
	}
	if !slices.Equal(types, []string{"mouseMoved", "mousePressed", "mouseReleased", "mousePressed", "mouseReleased"}) {
		t.Errorf("mouse events %v", types)
	}
	if ev[3]["clickCount"].(float64) != 2 {
		t.Errorf("double click count %v", ev[3]["clickCount"])
	}
	d.mu.Lock()
	last := d.events[len(d.events)-1]
	d.mu.Unlock()
	if last.Action != "click" || last.X != 110 || last.Y != 290 || !last.Double || last.Button != "left" {
		t.Errorf("click event %+v", last)
	}
	if _, err := b.Click(ctx, "e7", false, ""); !errors.Is(err, ErrNoRef) {
		t.Errorf("unknown ref: %v", err)
	}
	if _, err := b.Click(ctx, "", false, ""); err == nil {
		t.Error("empty ref accepted")
	}
	if _, err := b.Click(ctx, "e1", false, "side"); err == nil {
		t.Error("bad button accepted")
	}
	f.SetEval(func(_ *cdptest.Target, name, _ string) any {
		if name == "locate" {
			return map[string]any{"found": true, "desc": "button \"OK\"", "covered": "div \"Cookie banner\"", "x": 1, "y": 1}
		}
		return nil
	})
	if out := ok(t)(b.Click(ctx, "e2", false, "")); !strings.Contains(out, `div "Cookie banner" covers the element`) {
		t.Errorf("covered note missing:\n%s", out)
	}
}

func TestClickFollowsNewTab(t *testing.T) {
	b, f, _ := newTest(t)
	f.AddTarget("https://example.com/", "Example Domain", true)
	f.SetEval(func(_ *cdptest.Target, name, _ string) any {
		if name == "locate" && f.Targets() == 1 {
			f.AddTarget("https://popup.example/", "Popup", false)
		}
		return nil
	})
	out := ok(t)(b.Click(context.Background(), "e1", false, ""))
	if !strings.Contains(out, "opened a new tab") || !strings.Contains(out, "Tab 2: Popup") {
		t.Errorf("new tab not followed:\n%s", out)
	}
}

func TestTypeSelectPressScroll(t *testing.T) {
	b, f, d := newTest(t)
	f.AddTarget("https://example.com/", "Example", true)
	ctx := context.Background()
	out := ok(t)(b.Type(ctx, "e1", "hello", true, true))
	if !strings.Contains(out, `Replaced the value of e1 link "Learn more" with 5 characters and pressed Enter`) {
		t.Errorf("type output:\n%s", out)
	}
	if ins := f.Called("Input.insertText"); len(ins) != 1 || ins[0]["text"] != "hello" {
		t.Errorf("insertText %v", ins)
	}
	keys := f.Called("Input.dispatchKeyEvent")
	if len(keys) != 2 || keys[0]["type"] != "keyDown" || keys[0]["text"] != "\r" || keys[1]["type"] != "keyUp" {
		t.Errorf("Enter events %v", keys)
	}
	f.SetEval(func(_ *cdptest.Target, name, _ string) any {
		if name == "focus" {
			return map[string]any{"found": true, "desc": "textbox \"Password\"", "password": true, "focused": true}
		}
		return nil
	})
	ok(t)(b.Type(ctx, "e2", "s3cret", false, false))
	d.mu.Lock()
	last := d.events[len(d.events)-1]
	d.mu.Unlock()
	if last.Action != "type" || last.Text != "••••••" {
		t.Errorf("password not masked in the event: %+v", last)
	}
	f.SetEval(nil)
	if _, err := b.Type(ctx, "", "", false, false); err == nil {
		t.Error("empty type accepted")
	}

	if out := ok(t)(b.Select(ctx, "e1", []string{"Blue"})); out != `Selected Blue in e1 combobox "Color"` {
		t.Errorf("select output %q", out)
	}
	f.SetEval(func(_ *cdptest.Target, name, _ string) any {
		if name == "select" {
			return map[string]any{"found": true, "desc": "combobox", "missing": []string{"Pink"}, "options": []string{"Red", "Blue"}}
		}
		return nil
	})
	if _, err := b.Select(ctx, "e1", []string{"Pink"}); err == nil || !strings.Contains(err.Error(), "options: Red | Blue") {
		t.Errorf("missing option: %v", err)
	}
	f.SetEval(nil)

	ok(t)(b.Press(ctx, "ctrl+a", ""))
	keys = f.Called("Input.dispatchKeyEvent")
	down := keys[len(keys)-2]
	if down["type"] != "rawKeyDown" || down["modifiers"].(float64) != modCtrl || down["code"] != "KeyA" ||
		down["commands"].([]any)[0] != "selectAll" {
		t.Errorf("ctrl+a event %v", down)
	}
	if _, err := b.Press(ctx, "hyper+q", ""); err == nil {
		t.Error("bad modifier accepted")
	}

	out = ok(t)(b.Scroll(ctx, "", 0, 600))
	wheel := f.Called("Input.dispatchMouseEvent")
	if w := wheel[len(wheel)-1]; w["type"] != "mouseWheel" || w["deltaY"].(float64) != 600 || w["x"].(float64) != 640 {
		t.Errorf("wheel event %v", w)
	}
	if !strings.Contains(out, "y=400 of 3000") {
		t.Errorf("scroll output:\n%s", out)
	}
	if out := ok(t)(b.Scroll(ctx, "e1", 0, 0)); !strings.HasPrefix(out, `Scrolled e1 link "Learn more" into view`) {
		t.Errorf("scroll ref output:\n%s", out)
	}
	if _, err := b.Scroll(ctx, "", 0, 0); err == nil {
		t.Error("empty scroll accepted")
	}
}

func TestTakeoverBlocksActionsButNotReads(t *testing.T) {
	b, f, d := newTest(t)
	f.AddTarget("https://example.com/", "Example", true)
	d.takeover = true
	ctx := context.Background()
	for name, run := range map[string]func() (string, error){
		"open":  func() (string, error) { return b.Open(ctx, "https://x.example", false) },
		"click": func() (string, error) { return b.Click(ctx, "e1", false, "") },
		"type":  func() (string, error) { return b.Type(ctx, "e1", "x", false, false) },
		"press": func() (string, error) { return b.Press(ctx, "Enter", "") },
		"eval":  func() (string, error) { return b.Eval(ctx, "1") },
		"back":  func() (string, error) { return b.History(ctx, -1) },
	} {
		if _, err := run(); !errors.Is(err, x11.ErrTakeover) {
			t.Errorf("%s during takeover: %v", name, err)
		}
	}
	if n := len(f.Called("Input.dispatchMouseEvent")) + len(f.Called("Input.insertText")) + len(f.Called("Page.navigate")); n != 0 {
		t.Errorf("%d input calls reached chrome during takeover", n)
	}
	ok(t)(b.Snapshot(ctx, 0))
	ok(t)(b.Text(ctx, "", 0))
	if _, _, err := b.Screenshot(ctx, false); err != nil {
		t.Fatal(err)
	}
}

func TestEvalTextScreenshotWaitHistory(t *testing.T) {
	b, f, _ := newTest(t)
	tgt := f.AddTarget("https://example.com/", "Example", true)
	ctx := context.Background()
	if out := ok(t)(b.Eval(ctx, "document.title")); out != "{\n  \"expr\": \"document.title\"\n}" {
		t.Errorf("eval output %q", out)
	}
	if out := ok(t)(b.Eval(ctx, "undefined")); out != "undefined" {
		t.Errorf("undefined %q", out)
	}
	if _, err := b.Eval(ctx, "throw new Error('boom')"); err == nil || err.Error() != "javascript error: Error: boom" {
		t.Errorf("exception: %v", err)
	}
	if out := ok(t)(b.Text(ctx, "", 10)); !strings.Contains(out, "(10 of 5000 characters") {
		t.Errorf("text output %q", out)
	}
	png, desc, err := b.Screenshot(ctx, true)
	if err != nil || string(png[:4]) != "\x89PNG" || !strings.Contains(desc, "1280x3000") {
		t.Errorf("screenshot %q %v", desc, err)
	}
	shot := f.Called("Page.captureScreenshot")[0]
	if shot["captureBeyondViewport"] != true {
		t.Errorf("full page params %v", shot)
	}
	ok(t)(b.Wait(ctx, "load", "", time.Second))
	if _, err := b.Wait(ctx, "selector", "#never", 30*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("wait timeout: %v", err)
	}
	if _, err := b.Wait(ctx, "color", "x", time.Second); err == nil {
		t.Error("bad wait kind accepted")
	}
	if _, err := b.History(ctx, -1); err == nil || !strings.Contains(err.Error(), "no page to go back") {
		t.Errorf("back without history: %v", err)
	}
	ok(t)(b.Open(ctx, "https://two.example/", false))
	if out := ok(t)(b.History(ctx, -1)); !strings.Contains(out, "Went back") || f.URLOf(tgt) != "https://example.com/" {
		t.Errorf("back: %s (url %s)", out, f.URLOf(tgt))
	}
	ok(t)(b.History(ctx, 1))
	if f.URLOf(tgt) != "https://two.example/" {
		t.Errorf("forward url %s", f.URLOf(tgt))
	}
}

func TestReattachAndReconnect(t *testing.T) {
	b, f, _ := newTest(t)
	f.AddTarget("https://example.com/", "Example", true)
	ctx := context.Background()
	ok(t)(b.Snapshot(ctx, 0))
	// Chrome drops the session (a crashed renderer): attach again.
	f.DropSessions()
	ok(t)(b.Snapshot(ctx, 0))
	// Chrome restarts: the next call connects again.
	b.mu.Lock()
	b.c.close()
	<-b.c.done
	b.mu.Unlock()
	ok(t)(b.Snapshot(ctx, 0))
	if n := len(f.Called("Target.attachToTarget")); n < 3 {
		t.Errorf("attach count %d", n)
	}
}

func TestParseCombo(t *testing.T) {
	cases := []struct {
		in         string
		key, code  string
		vk, mods   int
		text, fail string
	}{
		{in: "Enter", key: "Enter", code: "Enter", vk: 13, text: "\r"},
		{in: "return", key: "Enter", code: "Enter", vk: 13, text: "\r"},
		{in: "shift+Tab", key: "Tab", code: "Tab", vk: 9, mods: modShift},
		{in: "ctrl+shift+t", key: "T", code: "KeyT", vk: 84, mods: modCtrl | modShift},
		{in: "a", key: "a", code: "KeyA", vk: 65, text: "a"},
		{in: "shift+a", key: "A", code: "KeyA", vk: 65, mods: modShift, text: "A"},
		{in: "7", key: "7", code: "Digit7", vk: 55, text: "7"},
		{in: "F5", key: "F5", code: "F5", vk: 116},
		{in: "esc", key: "Escape", code: "Escape", vk: 27},
		{in: "ctrl++", key: "+", mods: modCtrl},
		{in: "/", key: "/", text: "/"},
		{in: "", fail: "required"},
		{in: "Foo", fail: "unknown key"},
		{in: "hyper+a", fail: "unknown modifier"},
	}
	for _, c := range cases {
		k, err := parseCombo(c.in)
		if c.fail != "" {
			if err == nil || !strings.Contains(err.Error(), c.fail) {
				t.Errorf("%q: error %v, want %q", c.in, err, c.fail)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if k.key != c.key || k.code != c.code || k.vk != c.vk || k.mods != c.mods || k.text != c.text {
			t.Errorf("%q: got %+v", c.in, k)
		}
	}
}

func TestScriptArgsAndURLs(t *testing.T) {
	s := script("text", "const a = ARG0, b = ARG1;", "ARG1 \"q\"", 3)
	if !strings.HasPrefix(s, "/*lbx:text*/(() => {") || !strings.Contains(s, `const a = "ARG1 \"q\"", b = 3;`) {
		t.Errorf("script %s", s[strings.LastIndex(s, "const a"):])
	}
	for in, want := range map[string]string{
		"example.com": "https://example.com", "http://x.test/a": "http://x.test/a", "about:blank": "about:blank",
		" https://e.com ": "https://e.com",
	} {
		if got, err := normalizeURL(in); err != nil || got != want {
			t.Errorf("normalizeURL(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normalizeURL(" "); err == nil {
		t.Error("empty url accepted")
	}
	if wheelClicks(250) != 2 || wheelClicks(-50) != -1 || wheelClicks(0) != 0 {
		t.Error("wheel clicks")
	}
}
