// Package mcp is the guest Computer Use MCP server: JSON-RPC 2.0 over HTTP
// POST on http://127.0.0.1:8910/mcp (the Streamable HTTP transport with JSON
// responses) with screenshot, mouse, keyboard, app launch, browser (package
// browser), Chrome CDP, and Blender tools. The host relays the same tools over the guest RPC
// (mcp.call) through Server.CallTool.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/lydiabox/lydiabox/internal/guest/browser"
	"github.com/lydiabox/lydiabox/internal/version"
)

// DefaultAddr is the listen address of the guest MCP server.
const DefaultAddr = "127.0.0.1:8910"

// Supported MCP protocol revisions, newest first.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Desktop is the X11 controller seen by the tools.
type Desktop interface {
	Screenshot(ctx context.Context, tool string) (*image.RGBA, error)
	Click(ctx context.Context, tool string, x, y int, button string, double bool) error
	MoveTo(ctx context.Context, tool string, x, y int) error
	Drag(ctx context.Context, tool string, fromX, fromY, x, y int, button string) error
	Scroll(ctx context.Context, tool string, x, y, dx, dy int) error
	TypeText(ctx context.Context, tool, text string) error
	KeyCombo(ctx context.Context, tool, combo string) error
	Launched(tool, app string)
}

// Apps launches desktop applications.
type Apps interface {
	// Launch starts chrome, blender, or terminal.
	Launch(ctx context.Context, app string) error
	// CDPEndpoint returns Chrome's DevTools WebSocket URL, starting Chrome
	// when it is not running.
	CDPEndpoint(ctx context.Context) (string, error)
	// RunBlender runs a Python script in Blender and returns its output.
	RunBlender(ctx context.Context, req BlenderRequest) (BlenderResult, error)
}

// Server is the MCP server.
type Server struct {
	desktop Desktop
	apps    Apps
	browser *browser.Browser
	tools   map[string]tool
	order   []string
}

// NewServer returns a server for desktop and apps.
func NewServer(desktop Desktop, apps Apps, opts ...Option) *Server {
	s := &Server{desktop: desktop, apps: apps, tools: map[string]tool{}}
	for _, o := range opts {
		o(s)
	}
	for _, t := range append(s.toolSet(), s.browserTools()...) {
		s.tools[t.Name] = t
		s.order = append(s.order, t.Name)
	}
	return s
}

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ServeHTTP implements the MCP endpoint. Only same-host origins are
// accepted, which stops web pages opened in the guest browser from driving
// the desktop (DNS rebinding and cross-site requests).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !localOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	out, status := s.HandleMessage(r.Context(), body)
	if out == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(out)
}

func localOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// HandleMessage processes one JSON-RPC message or batch and returns the
// encoded response (nil when only notifications were sent) and the HTTP
// status.
func (s *Server) HandleMessage(ctx context.Context, body []byte) ([]byte, int) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil || len(batch) == 0 {
			return mustJSON(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "invalid batch"}}), http.StatusBadRequest
		}
		var resps []rpcResponse
		for _, m := range batch {
			if resp := s.handleOne(ctx, m); resp != nil {
				resps = append(resps, *resp)
			}
		}
		if len(resps) == 0 {
			return nil, http.StatusAccepted
		}
		return mustJSON(resps), http.StatusOK
	}
	resp := s.handleOne(ctx, body)
	if resp == nil {
		return nil, http.StatusAccepted
	}
	return mustJSON(resp), http.StatusOK
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32603, err.Error()}})
	}
	return b
}

func (s *Server) handleOne(ctx context.Context, raw json.RawMessage) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{codeParse, "parse error: " + err.Error()}}
	}
	isNotification := len(req.ID) == 0
	if req.JSONRPC != "2.0" || req.Method == "" {
		if isNotification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidRequest, "invalid JSON-RPC 2.0 request"}}
	}
	result, rerr := s.dispatch(ctx, req.Method, req.Params)
	if isNotification {
		return nil
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	return resp
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(params, &p)
		ver := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				ver = v
			}
		}
		return map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "lydiabox-computer-use", "version": version.Get().Version},
			"instructions": "For web pages, use the browser_* tools: they drive the visible desktop Chrome. Call " +
				"browser_snapshot to get element refs, then act by ref. For other applications, use the computer_* " +
				"tools: screen coordinates are pixels of the 1920x1080 guest desktop; take a screenshot before acting. " +
				"While the user has taken over the desktop, input tools wait up to 30 s and then report it.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "notifications/initialized", "notifications/cancelled":
		return nil, nil
	case "tools/list":
		return map[string]any{"tools": s.Tools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{codeInvalidParams, "tools/call needs a tool name"}
		}
		res, err := s.CallTool(ctx, p.Name, p.Arguments)
		if err != nil {
			return nil, &rpcError{codeInvalidParams, err.Error()}
		}
		return res, nil
	}
	return nil, &rpcError{codeMethodNotFound, "method not found: " + method}
}

// ErrUnknownTool is returned by CallTool for a name not in tools/list.
var ErrUnknownTool = errors.New("unknown tool")

// Tools returns the tools/list entries.
func (s *Server) Tools() []ToolInfo {
	out := make([]ToolInfo, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.tools[n].ToolInfo)
	}
	return out
}

// CallTool runs a tool. Tool failures (bad arguments, a desktop under user
// takeover) are reported inside the result with isError, as MCP requires;
// the error return is only for unknown tools.
func (s *Server) CallTool(ctx context.Context, name string, args json.RawMessage) (*ToolResult, error) {
	t, ok := s.tools[name]
	if !ok {
		return nil, errors.Join(ErrUnknownTool, errors.New(name))
	}
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := t.run(ctx, args)
	if err != nil {
		return errorResult(err), nil
	}
	return res, nil
}
