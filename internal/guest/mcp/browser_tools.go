package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/browser"
)

// Option configures a Server.
type Option func(*Server)

// WithBrowser adds the browser_* tools backed by b.
func WithBrowser(b *browser.Browser) Option {
	return func(s *Server) { s.browser = b }
}

var (
	boolProp = func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	refProp  = strProp("element ref from browser_snapshot, for example e5")
)

func (s *Server) browserTools() []tool {
	if s.browser == nil {
		return nil
	}
	return []tool{
		{ToolInfo{"browser_open", "Open a URL in the desktop Chrome (current tab, or a new tab) and wait for the page to load.", schema(map[string]any{
			"url": strProp("URL; https:// is added to a bare host name"), "new_tab": boolProp("open in a new tab"),
		}, "url")}, s.browserOpen},
		{ToolInfo{"browser_back", "Go back in the current tab.", schema(map[string]any{})}, s.browserHistory(-1)},
		{ToolInfo{"browser_forward", "Go forward in the current tab.", schema(map[string]any{})}, s.browserHistory(1)},
		{ToolInfo{"browser_tabs", "List the Chrome tabs (* is current), or select or close a tab by its number.", schema(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "select", "close"}, "default": "list"},
			"tab":    intProp("tab number from the list (close: default current tab)"),
		})}, s.browserTabs},
		{ToolInfo{"browser_snapshot", "List the headings and interactive elements of the current tab with refs ([e5]) for the other browser tools. Take one after each page change.", schema(map[string]any{
			"limit": intProp("maximum lines (default 400)"),
		})}, s.browserSnapshot},
		{ToolInfo{"browser_click", "Click an element by ref with real mouse events (scrolls it into view first).", schema(map[string]any{
			"ref": refProp, "double": boolProp("double click"), "button": buttonProp,
		}, "ref")}, s.browserClick},
		{ToolInfo{"browser_type", "Type text into an element by ref (or the focused element). clear replaces the value; submit presses Enter.", schema(map[string]any{
			"ref": refProp, "text": strProp("text to type"), "clear": boolProp("replace the current value"),
			"submit": boolProp("press Enter after typing"),
		})}, s.browserType},
		{ToolInfo{"browser_select", "Choose options of a <select> element by value or label.", schema(map[string]any{
			"ref": refProp, "values": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1},
		}, "ref", "values")}, s.browserSelect},
		{ToolInfo{"browser_press", "Press a key or combination in the current tab, for example Enter, Escape, Tab, ArrowDown, ctrl+a.", schema(map[string]any{
			"key": strProp("key name or combination joined with +"), "ref": strProp("optional element ref to focus first"),
		}, "key")}, s.browserPress},
		{ToolInfo{"browser_scroll", "Scroll an element into view by ref, or scroll the page by dx/dy CSS pixels (dy > 0 is down).", schema(map[string]any{
			"ref": strProp("element ref to scroll into view"), "dx": intProp("horizontal pixels"), "dy": intProp("vertical pixels"),
		})}, s.browserScroll},
		{ToolInfo{"browser_text", "Read the visible text of the current tab, or of one element by ref.", schema(map[string]any{
			"ref": strProp("optional element ref"), "max_chars": intProp("default 20000"),
		})}, s.browserText},
		{ToolInfo{"browser_eval", "Evaluate a JavaScript expression in the current tab (promises are awaited) and return the result as JSON.", schema(map[string]any{
			"expression": strProp("JavaScript expression"),
		}, "expression")}, s.browserEval},
		{ToolInfo{"browser_screenshot", "Capture the current tab as PNG (the viewport, or the full page).", schema(map[string]any{
			"full_page": boolProp("capture the whole page"),
		})}, s.browserScreenshot},
		{ToolInfo{"browser_wait", "Wait until the page has loaded, a CSS selector is visible, the page text contains a value, or the URL contains a value.", schema(map[string]any{
			"for":             map[string]any{"type": "string", "enum": []string{"load", "selector", "text", "url"}, "default": "load"},
			"value":           strProp("selector, text, or URL part"),
			"timeout_seconds": intProp("default 30, maximum 300"),
		})}, s.browserWait},
	}
}

func (s *Server) browserOpen(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		URL    string `json:"url"`
		NewTab bool   `json:"new_tab"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Open(ctx, a.URL, a.NewTab))
}

func (s *Server) browserHistory(delta int) func(context.Context, json.RawMessage) (*ToolResult, error) {
	return func(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
		if err := decode(args, &struct{}{}); err != nil {
			return nil, err
		}
		return textOf(s.browser.History(ctx, delta))
	}
}

func (s *Server) browserTabs(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Action string `json:"action"`
		Tab    int    `json:"tab"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	tab := ""
	if a.Tab > 0 {
		tab = strconv.Itoa(a.Tab)
	}
	switch a.Action {
	case "", "list":
		return textOf(s.browser.Tabs(ctx))
	case "select":
		if tab == "" {
			return nil, errors.New("tab is required to select a tab")
		}
		return textOf(s.browser.SelectTab(ctx, tab))
	case "close":
		return textOf(s.browser.CloseTab(ctx, tab))
	}
	return nil, errors.New("action must be list, select, or close")
}

func (s *Server) browserSnapshot(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Limit int `json:"limit"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Snapshot(ctx, a.Limit))
}

func (s *Server) browserClick(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Ref    string `json:"ref"`
		Double bool   `json:"double"`
		Button string `json:"button"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Click(ctx, a.Ref, a.Double, a.Button))
}

func (s *Server) browserType(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Ref    string `json:"ref"`
		Text   string `json:"text"`
		Clear  bool   `json:"clear"`
		Submit bool   `json:"submit"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Type(ctx, a.Ref, a.Text, a.Clear, a.Submit))
}

func (s *Server) browserSelect(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Ref    string   `json:"ref"`
		Values []string `json:"values"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Select(ctx, a.Ref, a.Values))
}

func (s *Server) browserPress(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Key string `json:"key"`
		Ref string `json:"ref"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Press(ctx, a.Key, a.Ref))
}

func (s *Server) browserScroll(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Ref string `json:"ref"`
		DX  int    `json:"dx"`
		DY  int    `json:"dy"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Scroll(ctx, a.Ref, a.DX, a.DY))
}

func (s *Server) browserText(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Ref      string `json:"ref"`
		MaxChars int    `json:"max_chars"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Text(ctx, a.Ref, a.MaxChars))
}

func (s *Server) browserEval(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Expression string `json:"expression"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return textOf(s.browser.Eval(ctx, a.Expression))
}

func (s *Server) browserScreenshot(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		FullPage bool `json:"full_page"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	png, desc, err := s.browser.Screenshot(ctx, a.FullPage)
	if err != nil {
		return nil, err
	}
	return &ToolResult{Content: []Content{
		{Type: "image", Data: base64.StdEncoding.EncodeToString(png), MimeType: "image/png"},
		{Type: "text", Text: desc},
	}}, nil
}

func (s *Server) browserWait(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		For            string `json:"for"`
		Value          string `json:"value"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.TimeoutSeconds < 0 {
		return nil, fmt.Errorf("timeout_seconds must not be negative")
	}
	return textOf(s.browser.Wait(ctx, a.For, a.Value, time.Duration(a.TimeoutSeconds)*time.Second))
}

func textOf(s string, err error) (*ToolResult, error) {
	if err != nil {
		return nil, err
	}
	return &ToolResult{Content: []Content{{Type: "text", Text: s}}}, nil
}
