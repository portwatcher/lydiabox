// Package browser is high-level browser use for agents: it drives the
// visible desktop Chrome (started with a CDP port by the guest MCP
// LocalApps) through a small pure-Go Chrome DevTools Protocol client.
// The guest MCP server exposes it as browser_* tools, and the lbx CLI
// reaches the same tools over HTTP, so every harness gets one
// implementation with one set of element refs.
//
// Actions that change the page (navigate, click, type, key presses) run
// through Desktop.Act: they wait while the user has taken over the
// desktop, never interleave with other agent input, and appear in the
// agent_action stream. Reads (snapshot, text, screenshot, tab list) are
// allowed during a takeover.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/x11"
)

// Desktop gates and reports agent actions (x11.Controller).
type Desktop interface {
	// Act waits until the agent has control and runs fn with the input
	// lock held; it publishes ev (when ev.Action is set) after success.
	Act(ctx context.Context, ev x11.AgentAction, fn func() error) error
	// Observe publishes an action that injects no input.
	Observe(ev x11.AgentAction)
}

// Options configures a Browser.
type Options struct {
	// Endpoint returns the browser-level DevTools WebSocket URL, starting
	// Chrome when it is not running (mcp.LocalApps.CDPEndpoint).
	Endpoint func(ctx context.Context) (string, error)
	Desktop  Desktop
	// LoadTimeout bounds the wait for a page load after a navigation
	// (default 30 s). A slow page is not an error.
	LoadTimeout time.Duration
	// SettleTimeout bounds the wait for a navigation that a click or a
	// key press started (default 10 s).
	SettleTimeout time.Duration
	// Poll is the polling interval of waits (default 100 ms).
	Poll time.Duration
}

// Browser is the browser-use service. Operations are serialized.
type Browser struct {
	opts Options

	mu       sync.Mutex
	c        *conn
	sessions map[string]string // target ID -> flattened session ID
	tabIDs   map[string]int    // target ID -> tab number shown to agents
	nextTab  int
	active   string // target ID of the tab the agent works in
}

// ErrNoRef is returned for an element ref that is not in the page.
var ErrNoRef = errors.New("element ref not found; the page changed or the ref is from an older page: take a new browser snapshot")

// New returns a Browser.
func New(opts Options) *Browser {
	if opts.LoadTimeout <= 0 {
		opts.LoadTimeout = 30 * time.Second
	}
	if opts.SettleTimeout <= 0 {
		opts.SettleTimeout = 10 * time.Second
	}
	if opts.Poll <= 0 {
		opts.Poll = 100 * time.Millisecond
	}
	return &Browser{opts: opts, sessions: map[string]string{}, tabIDs: map[string]int{}}
}

// Close drops the DevTools connection; Chrome keeps running.
func (b *Browser) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c != nil {
		b.c.close()
		b.c = nil
	}
}

func (b *Browser) connect(ctx context.Context) (*conn, error) {
	if b.c != nil && b.c.alive() {
		return b.c, nil
	}
	if b.opts.Endpoint == nil {
		return nil, errors.New("no chrome devtools endpoint configured")
	}
	ws, err := b.opts.Endpoint(ctx)
	if err != nil {
		return nil, err
	}
	c, err := dial(ctx, ws)
	if err != nil {
		return nil, err
	}
	b.c = c
	b.sessions = map[string]string{}
	return c, nil
}

// tab is one page target.
type tab struct {
	ID       int
	TargetID string
	Title    string
	URL      string
}

// tabs lists the page targets in tab-number order. Tab numbers stay the
// same while a tab is open.
func (b *Browser) tabs(ctx context.Context) ([]tab, error) {
	c, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	var res struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := c.call(ctx, "", "Target.getTargets", nil, &res); err != nil {
		return nil, err
	}
	var out []tab
	open := map[string]bool{}
	for _, t := range res.TargetInfos {
		if t.Type != "page" || strings.HasPrefix(t.URL, "devtools://") {
			continue
		}
		open[t.TargetID] = true
		id, ok := b.tabIDs[t.TargetID]
		if !ok {
			b.nextTab++
			id = b.nextTab
			b.tabIDs[t.TargetID] = id
		}
		out = append(out, tab{ID: id, TargetID: t.TargetID, Title: t.Title, URL: t.URL})
	}
	for tid := range b.tabIDs {
		if !open[tid] {
			delete(b.tabIDs, tid)
			delete(b.sessions, tid)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// page is an attached tab.
type page struct {
	b   *Browser
	tab tab
}

func (b *Browser) attach(ctx context.Context, targetID string) (string, error) {
	if sid, ok := b.sessions[targetID]; ok {
		return sid, nil
	}
	c, err := b.connect(ctx)
	if err != nil {
		return "", err
	}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, &res); err != nil {
		return "", err
	}
	b.sessions[targetID] = res.SessionID
	return res.SessionID, nil
}

// call sends a page command; a stale session is attached again once.
func (p *page) call(ctx context.Context, method string, params, out any) error {
	for attempt := 0; ; attempt++ {
		sid, err := p.b.attach(ctx, p.tab.TargetID)
		if err != nil {
			return err
		}
		err = p.b.c.call(ctx, sid, method, params, out)
		if err != nil && attempt == 0 && isSessionGone(err) {
			delete(p.b.sessions, p.tab.TargetID)
			continue
		}
		return err
	}
}

type remoteObject struct {
	Type                string          `json:"type"`
	Subtype             string          `json:"subtype"`
	Value               json.RawMessage `json:"value"`
	UnserializableValue string          `json:"unserializableValue"`
	Description         string          `json:"description"`
}

// evaluate runs expression in the page and returns the result object.
func (p *page) evaluate(ctx context.Context, expression string) (remoteObject, error) {
	var res struct {
		Result           remoteObject `json:"result"`
		ExceptionDetails *struct {
			Text      string        `json:"text"`
			Exception *remoteObject `json:"exception"`
		} `json:"exceptionDetails"`
	}
	err := p.call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true, "userGesture": true,
	}, &res)
	if err != nil {
		return remoteObject{}, err
	}
	if d := res.ExceptionDetails; d != nil {
		msg := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			msg = d.Exception.Description
		}
		return remoteObject{}, fmt.Errorf("javascript error: %s", firstLine(msg))
	}
	return res.Result, nil
}

// eval runs a page script and decodes its value into out.
func (p *page) eval(ctx context.Context, expression string, out any) error {
	r, err := p.evaluate(ctx, expression)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if len(r.Value) == 0 {
		return fmt.Errorf("page script returned no value (%s)", r.Type)
	}
	return json.Unmarshal(r.Value, out)
}

type pageStatus struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Ready   string `json:"ready"`
	Visible bool   `json:"visible"`
}

func (p *page) status(ctx context.Context) (pageStatus, error) {
	var st pageStatus
	err := p.eval(ctx, script("status", statusBody), &st)
	return st, err
}

// current returns the tab the agent works in: the remembered tab while it
// is on screen, else the tab the user sees (so the agent follows a tab
// switch made during a takeover), else the remembered or the last tab. A
// browser without tabs gets a new blank tab.
func (b *Browser) current(ctx context.Context) (*page, error) {
	ts, err := b.tabs(ctx)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		if _, err := b.newTab(ctx, "about:blank"); err != nil {
			return nil, err
		}
		if ts, err = b.tabs(ctx); err != nil {
			return nil, err
		}
		if len(ts) == 0 {
			return nil, errors.New("chrome has no tab")
		}
	}
	var remembered *page
	for _, t := range ts {
		if t.TargetID == b.active {
			remembered = &page{b: b, tab: t}
		}
	}
	if remembered != nil {
		if st, err := remembered.status(ctx); err == nil && st.Visible {
			return remembered, nil
		}
	}
	for _, t := range ts {
		if remembered != nil && t.TargetID == remembered.tab.TargetID {
			continue
		}
		p := &page{b: b, tab: t}
		if st, err := p.status(ctx); err == nil && st.Visible {
			b.active = t.TargetID
			return p, nil
		}
	}
	if remembered != nil {
		return remembered, nil
	}
	last := ts[len(ts)-1]
	b.active = last.TargetID
	return &page{b: b, tab: last}, nil
}

func (b *Browser) newTab(ctx context.Context, u string) (string, error) {
	c, err := b.connect(ctx)
	if err != nil {
		return "", err
	}
	var res struct {
		TargetID string `json:"targetId"`
	}
	if err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": u}, &res); err != nil {
		return "", err
	}
	b.active = res.TargetID
	return res.TargetID, nil
}

// pageByTarget returns an attached page for targetID.
func (b *Browser) pageByTarget(ctx context.Context, targetID string) (*page, error) {
	ts, err := b.tabs(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range ts {
		if t.TargetID == targetID {
			return &page{b: b, tab: t}, nil
		}
	}
	return nil, errors.New("the tab closed")
}

// waitFor polls cond until it is true or timeout passes. Errors of cond
// (a document that is being replaced) count as false.
func (b *Browser) waitFor(ctx context.Context, timeout time.Duration, cond func() (bool, error)) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(b.opts.Poll)
	defer t.Stop()
	for {
		if ok, err := cond(); err == nil && ok {
			return true, nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return false, nil
			}
			return false, ctx.Err()
		case <-t.C:
		}
	}
}

func (b *Browser) waitLoad(ctx context.Context, p *page, timeout time.Duration) (bool, error) {
	return b.waitFor(ctx, timeout, func() (bool, error) {
		var ok bool
		err := p.eval(ctx, script("wait", waitBody, "load", ""), &ok)
		return ok, err
	})
}

// describePage is the short page line returned after an action.
func (b *Browser) describePage(ctx context.Context, p *page, loaded bool) string {
	st, err := p.status(ctx)
	if err != nil {
		return fmt.Sprintf("Tab %d: %s", p.tab.ID, p.tab.URL)
	}
	s := fmt.Sprintf("Tab %d: %s\nURL: %s", p.tab.ID, orNone(st.Title), st.URL)
	if !loaded || st.Ready != "complete" {
		s += "\nThe page is still loading."
	}
	return s
}

// normalizeURL adds https:// to a bare host name.
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	if !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "about:") && !strings.HasPrefix(raw, "data:") &&
		!strings.HasPrefix(raw, "chrome:") && !strings.HasPrefix(raw, "file:") {
		raw = "https://" + raw
	}
	if _, err := url.Parse(raw); err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}
	return raw, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(no title)"
	}
	return s
}

// parseTab parses a tab number ("2" or "tab 2"); "" is the current tab.
func parseTab(s string) (int, error) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "tab"))
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid tab %q: use the number from the tab list", s)
	}
	return n, nil
}
