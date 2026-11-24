package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/x11"
)

// Open loads rawURL in the current tab, or in a new tab, and waits for the
// load (up to LoadTimeout).
func (b *Browser) Open(ctx context.Context, rawURL string, newTab bool) (string, error) {
	u, err := normalizeURL(rawURL)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var p *page
	err = b.opts.Desktop.Act(ctx, x11.AgentAction{Action: "launch", Text: u, Tool: "browser_open"}, func() error {
		if newTab {
			tid, err := b.newTab(ctx, u)
			if err != nil {
				return err
			}
			if p, err = b.pageByTarget(ctx, tid); err != nil {
				return err
			}
			return b.activate(ctx, p)
		}
		if p, err = b.current(ctx); err != nil {
			return err
		}
		var res struct {
			ErrorText string `json:"errorText"`
		}
		if err := p.call(ctx, "Page.navigate", map[string]any{"url": u}, &res); err != nil {
			return err
		}
		if res.ErrorText != "" {
			return fmt.Errorf("navigation to %s failed: %s", u, res.ErrorText)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// Page.navigate returns before the old document is gone; a short
	// pause keeps the load check from reading the old document.
	sleep(ctx, 2*b.opts.Poll)
	loaded, err := b.waitLoad(ctx, p, b.opts.LoadTimeout)
	if err != nil {
		return "", err
	}
	return "Opened " + u + "\n" + b.describePage(ctx, p, loaded), nil
}

// History goes back (delta -1) or forward (delta 1) in the current tab.
func (b *Browser) History(ctx context.Context, delta int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tool, word := "browser_back", "back"
	if delta > 0 {
		tool, word = "browser_forward", "forward"
	}
	var p *page
	var target string
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		var err error
		if p, err = b.current(ctx); err != nil {
			return err
		}
		var h struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				ID  int    `json:"id"`
				URL string `json:"url"`
			} `json:"entries"`
		}
		if err := p.call(ctx, "Page.getNavigationHistory", nil, &h); err != nil {
			return err
		}
		i := h.CurrentIndex + delta
		if i < 0 || i >= len(h.Entries) {
			return fmt.Errorf("there is no page to go %s to", word)
		}
		target = h.Entries[i].URL
		return p.call(ctx, "Page.navigateToHistoryEntry", map[string]any{"entryId": h.Entries[i].ID}, nil)
	})
	if err != nil {
		return "", err
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "launch", Text: target, Tool: tool})
	sleep(ctx, 2*b.opts.Poll)
	loaded, err := b.waitLoad(ctx, p, b.opts.LoadTimeout)
	if err != nil {
		return "", err
	}
	return "Went " + word + "\n" + b.describePage(ctx, p, loaded), nil
}

func (b *Browser) activate(ctx context.Context, p *page) error {
	c, err := b.connect(ctx)
	if err != nil {
		return err
	}
	if err := c.call(ctx, "", "Target.activateTarget", map[string]any{"targetId": p.tab.TargetID}, nil); err != nil {
		return err
	}
	b.active = p.tab.TargetID
	return nil
}

// Tabs lists the open tabs; "*" marks the current tab.
func (b *Browser) Tabs(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, err := b.current(ctx)
	if err != nil {
		return "", err
	}
	ts, err := b.tabs(ctx)
	if err != nil {
		return "", err
	}
	return formatTabs(ts, cur.tab.TargetID), nil
}

func formatTabs(ts []tab, current string) string {
	var sb strings.Builder
	for _, t := range ts {
		mark := " "
		if t.TargetID == current {
			mark = "*"
		}
		fmt.Fprintf(&sb, "%s %d  %s  %s\n", mark, t.ID, orNone(t.Title), t.URL)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (b *Browser) findTab(ctx context.Context, id string) (*page, error) {
	n, err := parseTab(id)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return b.current(ctx)
	}
	ts, err := b.tabs(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.ID == n {
			return &page{b: b, tab: t}, nil
		}
	}
	return nil, fmt.Errorf("no tab %d; open tabs:\n%s", n, formatTabs(ts, b.active))
}

// SelectTab brings tab id to the front and makes it the current tab.
func (b *Browser) SelectTab(ctx context.Context, id string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var p *page
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		var err error
		if p, err = b.findTab(ctx, id); err != nil {
			return err
		}
		return b.activate(ctx, p)
	})
	if err != nil {
		return "", err
	}
	return "Selected " + b.describePage(ctx, p, true), nil
}

// CloseTab closes tab id ("" is the current tab).
func (b *Browser) CloseTab(ctx context.Context, id string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var closed tab
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		p, err := b.findTab(ctx, id)
		if err != nil {
			return err
		}
		closed = p.tab
		c, err := b.connect(ctx)
		if err != nil {
			return err
		}
		return c.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": p.tab.TargetID}, nil)
	})
	if err != nil {
		return "", err
	}
	if b.active == closed.TargetID {
		b.active = ""
	}
	delete(b.sessions, closed.TargetID)
	// Target.closeTarget returns before the tab is gone.
	var ts []tab
	_, err = b.waitFor(ctx, 5*time.Second, func() (bool, error) {
		var err error
		if ts, err = b.tabs(ctx); err != nil {
			return false, err
		}
		for _, t := range ts {
			if t.TargetID == closed.TargetID {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Closed tab %d (%s)\nOpen tabs:\n%s", closed.ID, closed.URL, formatTabs(ts, "")), nil
}

type snapshot struct {
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
	Scroll    [3]int   `json:"scroll"`
}

// DefaultSnapshotLimit is the default number of snapshot lines.
const DefaultSnapshotLimit = 400

// Snapshot lists the headings and the interactive elements of the current
// tab in document order. Each element has a ref ("[e5]") for Click, Type,
// Select, Press, Scroll, and Text.
func (b *Browser) Snapshot(ctx context.Context, limit int) (string, error) {
	if limit <= 0 {
		limit = DefaultSnapshotLimit
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, err := b.current(ctx)
	if err != nil {
		return "", err
	}
	var s snapshot
	if err := p.eval(ctx, script("snapshot", snapshotBody, limit), &s); err != nil {
		return "", err
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "screenshot", Tool: "browser_snapshot"})
	var sb strings.Builder
	fmt.Fprintf(&sb, "Tab %d: %s\nURL: %s\nScroll: y=%d of %d, viewport height %d\n", p.tab.ID, orNone(s.Title), s.URL, s.Scroll[0], s.Scroll[1], s.Scroll[2])
	if len(s.Lines) == 0 {
		sb.WriteString("(no headings or interactive elements; use browser_text to read the page)")
	}
	for _, l := range s.Lines {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	if s.Truncated {
		fmt.Fprintf(&sb, "… stopped after %d lines; use a larger limit or browser_text\n", limit)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// DefaultTextLimit is the default number of characters Text returns.
const DefaultTextLimit = 20000

// Text returns the visible text of the page, or of element ref.
func (b *Browser) Text(ctx context.Context, ref string, limit int) (string, error) {
	if limit <= 0 {
		limit = DefaultTextLimit
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p, err := b.current(ctx)
	if err != nil {
		return "", err
	}
	var r struct {
		Found bool   `json:"found"`
		Text  string `json:"text"`
		Total int    `json:"total"`
	}
	if err := p.eval(ctx, script("text", textBody, ref, limit), &r); err != nil {
		return "", err
	}
	if !r.Found {
		return "", fmt.Errorf("%s: %w", ref, ErrNoRef)
	}
	if r.Total > len([]rune(r.Text)) && r.Total > limit {
		r.Text += fmt.Sprintf("\n… (%d of %d characters; use a larger limit or an element ref)", limit, r.Total)
	}
	if r.Text == "" {
		return "(no text)", nil
	}
	return r.Text, nil
}

// Eval evaluates a JavaScript expression in the current tab (promises are
// awaited) and returns the result as JSON.
func (b *Browser) Eval(ctx context.Context, expression string) (string, error) {
	if strings.TrimSpace(expression) == "" {
		return "", errors.New("expression is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out string
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		p, err := b.current(ctx)
		if err != nil {
			return err
		}
		r, err := p.evaluate(ctx, expression)
		if err != nil {
			return err
		}
		out = formatRemote(r)
		return nil
	})
	return out, err
}

func formatRemote(r remoteObject) string {
	switch {
	case r.UnserializableValue != "":
		return r.UnserializableValue
	case r.Type == "undefined":
		return "undefined"
	case len(r.Value) > 0:
		var v any
		if json.Unmarshal(r.Value, &v) == nil {
			if s, ok := v.(string); ok {
				return s
			}
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				return string(b)
			}
		}
		return string(r.Value)
	case r.Description != "":
		return r.Description
	}
	return r.Type
}

// maxShotHeight bounds a full-page screenshot.
const maxShotHeight = 16384

// Screenshot captures the current tab as PNG: the viewport, or the whole
// page (up to 16384 pixels high) when fullPage is set.
func (b *Browser) Screenshot(ctx context.Context, fullPage bool) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, err := b.current(ctx)
	if err != nil {
		return nil, "", err
	}
	params := map[string]any{"format": "png"}
	var vp viewport
	if err := p.eval(ctx, script("viewport", viewportBody), &vp); err != nil {
		return nil, "", err
	}
	w, h := vp.W, vp.H
	if fullPage {
		w, h = max(vp.Width, vp.W), min(max(vp.Height, vp.H), maxShotHeight)
		params["captureBeyondViewport"] = true
		params["clip"] = map[string]any{"x": 0, "y": 0, "width": w, "height": h, "scale": 1}
	}
	var res struct {
		Data string `json:"data"`
	}
	if err := p.call(ctx, "Page.captureScreenshot", params, &res); err != nil {
		return nil, "", err
	}
	png, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, "", fmt.Errorf("decode screenshot: %w", err)
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "screenshot", Tool: "browser_screenshot"})
	return png, fmt.Sprintf("Tab %d page screenshot %dx%d (CSS pixels), scroll y=%d", p.tab.ID, w, h, vp.Y), nil
}

type viewport struct {
	W      int `json:"w"`
	H      int `json:"h"`
	X      int `json:"x"`
	Y      int `json:"y"`
	Height int `json:"height"`
	Width  int `json:"width"`
}

// Wait kinds.
var waitKinds = []string{"load", "selector", "text", "url"}

// Wait waits until the page has loaded ("load"), a CSS selector matches a
// visible element ("selector"), the page text contains value ("text"), or
// the URL contains value ("url").
func (b *Browser) Wait(ctx context.Context, kind, value string, timeout time.Duration) (string, error) {
	if kind == "" {
		kind = "load"
	}
	ok := false
	for _, k := range waitKinds {
		ok = ok || k == kind
	}
	if !ok {
		return "", fmt.Errorf("wait kind must be one of %s", strings.Join(waitKinds, ", "))
	}
	if kind != "load" && value == "" {
		return "", fmt.Errorf("wait %s needs a value", kind)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timeout = min(timeout, 5*time.Minute)
	b.mu.Lock()
	defer b.mu.Unlock()
	start := time.Now()
	var p *page
	met, err := b.waitFor(ctx, timeout, func() (bool, error) {
		var err error
		if p, err = b.current(ctx); err != nil {
			return false, err
		}
		var ok bool
		err = p.eval(ctx, script("wait", waitBody, kind, value), &ok)
		return ok, err
	})
	if err != nil {
		return "", err
	}
	if !met {
		return "", fmt.Errorf("timed out after %s waiting for %s %q", timeout, kind, value)
	}
	return fmt.Sprintf("Done waiting for %s after %.1f s\n%s", kind, time.Since(start).Seconds(), b.describePage(ctx, p, true)), nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
