// Package cdptest is a scripted Chrome DevTools Protocol endpoint for
// tests of package browser and of its users (the guest MCP server and the
// lbx CLI). Page scripts are answered by their /*lbx:<name>*/ marker.
package cdptest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// Target is one page of the fake browser. Read its fields through
// Server methods while the server runs.
type Target struct {
	ID, URL, Title string
	History        []string
	Index          int
	Visible        bool
}

// Server is a scripted Chrome DevTools endpoint. Page scripts are
// answered by their /*lbx:<name>*/ marker (see SetEval); other
// Runtime.evaluate expressions return Expr.
type Server struct {
	t   testing.TB
	srv *httptest.Server

	mu       sync.Mutex
	targets  []*Target
	sessions map[string]string // session -> target
	calls    []string          // "method" or "method@target"
	params   []map[string]any
	nextID   int
	eval     func(target *Target, name, expr string) any
	navError string
}

func New(t testing.TB) *Server {
	f := &Server{t: t, sessions: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// URL is the browser WebSocket URL.
func (f *Server) URL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/devtools/browser/fake"
}

// AddTarget adds a page.
func (f *Server) AddTarget(url, title string, visible bool) *Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	t := &Target{ID: "T" + string(rune('0'+f.nextID)), URL: url, Title: title, History: []string{url}, Visible: visible}
	f.targets = append(f.targets, t)
	return t
}

// Called returns the parameters of every call of method.
func (f *Server) Called(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for i, c := range f.calls {
		if strings.SplitN(c, "@", 2)[0] == method {
			out = append(out, f.params[i])
		}
	}
	return out
}

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "origin rejected", http.StatusForbidden)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(64 << 20)
	defer c.CloseNow()
	ctx := context.Background()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			ID        int64          `json:"id"`
			Method    string         `json:"method"`
			Params    map[string]any `json:"params"`
			SessionID string         `json:"sessionId"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			f.t.Errorf("bad request %s", data)
			return
		}
		result, cerr := f.handle(req.Method, req.SessionID, req.Params)
		resp := map[string]any{"id": req.ID}
		if cerr != "" {
			resp["error"] = map[string]any{"code": -32000, "message": cerr}
		} else {
			resp["result"] = result
		}
		if req.SessionID != "" {
			resp["sessionId"] = req.SessionID
		}
		// An unrelated event between responses must be ignored.
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"method":"Target.targetInfoChanged","params":{}}`))
		out, _ := json.Marshal(resp)
		if err := c.Write(ctx, websocket.MessageText, out); err != nil {
			return
		}
	}
}

var markerRE = regexp.MustCompile(`^/\*lbx:(\w+)\*/`)

func (f *Server) handle(method, session string, p map[string]any) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var tgt *Target
	if session != "" {
		id, ok := f.sessions[session]
		for _, t := range f.targets {
			if t.ID == id {
				tgt = t
			}
		}
		if !ok || tgt == nil {
			return nil, "Session with given id not found."
		}
		f.calls = append(f.calls, method+"@"+tgt.ID)
	} else {
		f.calls = append(f.calls, method)
	}
	f.params = append(f.params, p)
	switch method {
	case "Target.getTargets":
		var infos []map[string]any
		for _, t := range f.targets {
			infos = append(infos, map[string]any{"targetId": t.ID, "type": "page", "title": t.Title, "url": t.URL})
		}
		infos = append(infos, map[string]any{"targetId": "SW", "type": "service_worker", "url": "https://x/sw.js"})
		return map[string]any{"targetInfos": infos}, ""
	case "Target.attachToTarget":
		id := p["targetId"].(string)
		sid := "S-" + id + "-" + string(rune('a'+len(f.sessions)))
		f.sessions[sid] = id
		return map[string]any{"sessionId": sid}, ""
	case "Target.createTarget":
		f.mu.Unlock()
		t := f.AddTarget(p["url"].(string), "New", true)
		f.mu.Lock()
		for _, o := range f.targets {
			o.Visible = o == t
		}
		return map[string]any{"targetId": t.ID}, ""
	case "Target.activateTarget":
		for _, o := range f.targets {
			o.Visible = o.ID == p["targetId"]
		}
		return map[string]any{}, ""
	case "Target.closeTarget":
		for i, o := range f.targets {
			if o.ID == p["targetId"] {
				f.targets = append(f.targets[:i], f.targets[i+1:]...)
				break
			}
		}
		return map[string]any{"success": true}, ""
	case "Page.navigate":
		if f.navError != "" {
			return map[string]any{"errorText": f.navError}, ""
		}
		tgt.History = append(tgt.History[:tgt.Index+1], p["url"].(string))
		tgt.Index = len(tgt.History) - 1
		tgt.URL = p["url"].(string)
		return map[string]any{"frameId": "F"}, ""
	case "Page.getNavigationHistory":
		var entries []map[string]any
		for i, u := range tgt.History {
			entries = append(entries, map[string]any{"id": i + 100, "url": u, "title": u})
		}
		return map[string]any{"currentIndex": tgt.Index, "entries": entries}, ""
	case "Page.navigateToHistoryEntry":
		tgt.Index = int(p["entryId"].(float64)) - 100
		tgt.URL = tgt.History[tgt.Index]
		return map[string]any{}, ""
	case "Page.captureScreenshot":
		return map[string]any{"data": "iVBORw0KGgo="}, ""
	case "Input.dispatchMouseEvent", "Input.dispatchKeyEvent", "Input.insertText":
		return map[string]any{}, ""
	case "Runtime.evaluate":
		expr := p["expression"].(string)
		if m := markerRE.FindStringSubmatch(expr); m != nil {
			v := f.builtin(tgt, m[1], expr)
			if hook := f.eval; hook != nil {
				f.mu.Unlock()
				if got := hook(tgt, m[1], expr); got != nil {
					v = got
				}
				f.mu.Lock()
			}
			return map[string]any{"result": map[string]any{"type": "object", "value": v}}, ""
		}
		if strings.Contains(expr, "throw") {
			return map[string]any{"result": map[string]any{"type": "object"},
				"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "Error: boom\n    at <anonymous>:1:7"}}}, ""
		}
		if expr == "undefined" {
			return map[string]any{"result": map[string]any{"type": "undefined"}}, ""
		}
		return map[string]any{"result": map[string]any{"type": "object", "value": map[string]any{"expr": expr}}}, ""
	}
	return nil, "'" + method + "' wasn't found"
}

// builtin answers the page scripts with plain defaults.
func (f *Server) builtin(t *Target, name, expr string) any {
	switch name {
	case "status":
		return map[string]any{"title": t.Title, "url": t.URL, "ready": "complete", "visible": t.Visible}
	case "wait":
		return strings.Contains(expr, `"load"`)
	case "viewport":
		return map[string]any{"w": 1280, "h": 800, "x": 0, "y": 400, "height": 3000, "width": 1280}
	case "snapshot":
		return map[string]any{"title": t.Title, "url": t.URL, "lines": []string{`heading 1 "Example Domain"`,
			`[e1] link "Learn more" -> https://iana.org/domains/example`}, "truncated": false, "scroll": []int{0, 900, 800}}
	case "locate", "focus", "reveal":
		if strings.Contains(expr, `"e1"`) || strings.Contains(expr, `const ref = "", `) {
			return map[string]any{"found": true, "desc": `link "Learn more"`, "x": 100.5, "y": 200, "sx": 110, "sy": 290, "focused": true}
		}
		return map[string]any{"found": false}
	case "select":
		if strings.Contains(expr, `"e1"`) {
			return map[string]any{"found": true, "desc": `combobox "Color"`, "selected": []string{"Blue"}, "sx": 5, "sy": 6}
		}
		return map[string]any{"found": false}
	case "text":
		return map[string]any{"found": true, "text": "Example Domain\nThis domain is for use in examples.", "total": 5000}
	}
	return nil
}

// Lock runs fn with the server state locked (to change targets).
func (f *Server) Lock(fn func(targets []*Target)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.targets)
}

// URLOf returns the URL of t.
func (f *Server) URLOf(t *Target) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return t.URL
}

// DropSessions forgets every session, as after a renderer crash.
func (f *Server) DropSessions() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = map[string]string{}
}

// SetEval sets a hook that answers page scripts: it returns the JSON
// value, or nil for the built-in answer. It runs without the server lock.
func (f *Server) SetEval(fn func(t *Target, name, expr string) any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eval = fn
}

// SetNavError makes Page.navigate fail with errorText.
func (f *Server) SetNavError(errorText string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.navError = errorText
}

// Targets returns the number of pages.
func (f *Server) Targets() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.targets)
}
