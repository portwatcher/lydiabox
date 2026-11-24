package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/portwatcher/lydiabox/internal/connector"
)

// MCP protocol versions this server speaks, newest first.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

const maxMCPBody = 4 << 20

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

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	resp.JSONRPC = "2.0"
	if resp.ID == nil {
		resp.ID = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (b *Broker) mcp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMCPBody))
	if err != nil {
		writeRPC(w, rpcResponse{Error: &rpcError{Code: codeParseError, Message: "request body is unreadable or too large"}})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, rpcResponse{Error: &rpcError{Code: codeParseError, Message: "invalid JSON"}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, rpcResponse{ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "not a JSON-RPC 2.0 request"}})
		return
	}
	// Notifications (no id) and client responses get 202 without a body.
	if len(req.ID) == 0 || bytes.Equal(req.ID, []byte("null")) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := b.dispatch(r.Context(), req)
	writeRPC(w, rpcResponse{ID: req.ID, Result: result, Error: rerr})
}

func (b *Broker) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "lydiabox-connectors", "version": b.version},
			"instructions": "Tools for the user's connected GitHub, Google Drive, Notion, and Slack accounts. " +
				"Workspace paths are relative to /workspace.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(b.tools))
		for _, t := range b.tools {
			list = append(list, map[string]any{"name": t.name, "description": t.description, "inputSchema": t.inputSchema()})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a tool name"}
		}
		t := b.byName[p.Name]
		if t == nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool %q", p.Name)}
		}
		return b.callTool(ctx, t, p.Arguments), nil
	}
	return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("method %q is not supported", req.Method)}
}

// toolResult is an MCP CallToolResult.
type toolResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func errorResult(msg string) toolResult {
	return toolResult{Content: []textContent{{Type: "text", Text: msg}}, IsError: true}
}

// errInvalidArgs marks argument validation failures.
var errInvalidArgs = errors.New("invalid arguments")

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errInvalidArgs, fmt.Sprintf(format, a...))
}

// callTool runs a tool and always writes a connector.mcp.call audit entry.
func (b *Broker) callTool(ctx context.Context, t *tool, rawArgs json.RawMessage) toolResult {
	if len(rawArgs) == 0 || bytes.Equal(rawArgs, []byte("null")) {
		rawArgs = json.RawMessage("{}")
	}
	var common struct {
		Account string `json:"account"`
	}
	audit := connector.MCPCall{Tool: t.name, Provider: t.provider}
	finish := func(res toolResult, errMsg string) toolResult {
		audit.Error = errMsg
		b.mgr.RecordMCPCall(ctx, b.tenantID, audit)
		return res
	}
	if err := json.Unmarshal(rawArgs, &common); err != nil {
		msg := "arguments must be a JSON object"
		return finish(errorResult(msg), msg)
	}
	cred, err := b.mgr.Credential(ctx, b.tenantID, t.provider, common.Account)
	if err != nil {
		var msg string
		switch {
		case errors.Is(err, connector.ErrNotConnected), errors.Is(err, connector.ErrExpired):
			msg = err.Error() + ". Ask the user to connect it on the LydiaBox Connectors page."
		default:
			b.log.Error("connector credential lookup failed", "tool", t.name, "err", err)
			msg = "credential lookup failed"
		}
		return finish(errorResult(msg), msg)
	}
	audit.Account = cred.Account
	p, err := b.mgr.Provider(t.provider)
	if err != nil {
		return finish(errorResult(err.Error()), err.Error())
	}
	c := &call{b: b, ctx: ctx, cred: cred, p: p, http: b.mgr.HTTPClient()}
	out, err := t.run(c, rawArgs)
	if err != nil {
		var ae *connector.APIError
		if errors.As(err, &ae) && ae.Unauthorized() {
			b.mgr.MarkRevoked(ctx, b.tenantID, t.provider, cred.Account)
		}
		return finish(errorResult(err.Error()), err.Error())
	}
	text, ok := out.(string)
	if !ok {
		buf, jerr := json.MarshalIndent(out, "", "  ")
		if jerr != nil {
			return finish(errorResult("encode result: "+jerr.Error()), "encode result")
		}
		text = string(buf)
	}
	return finish(toolResult{Content: []textContent{{Type: "text", Text: text}}}, "")
}

// tool is one connector MCP tool.
type tool struct {
	name        string
	provider    string
	description string
	props       map[string]any
	required    []string
	run         func(c *call, args json.RawMessage) (any, error)
}

func (t tool) inputSchema() map[string]any {
	props := map[string]any{"account": prop("string", "Connected account label; defaults to the first connected account")}
	for k, v := range t.props {
		props[k] = v
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(t.required) > 0 {
		s["required"] = t.required
	}
	return s
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func enumProp(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func arrayProp(desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}}
}

// decodeArgs unmarshals tool arguments into v.
func decodeArgs(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return invalidf("%v", err)
	}
	return nil
}

// call is the context of one tool invocation.
type call struct {
	b    *Broker
	ctx  context.Context
	cred connector.Credential
	p    *connector.Provider
	http *http.Client
}

// do sends a JSON request with the account's bearer token and decodes the
// JSON answer into out.
func (c *call) do(method, rawURL string, in, out any, header map[string]string) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	req, err := connector.NewRequest(c.ctx, method, rawURL, c.cred.AccessToken, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if c.p.ID == connector.Slack {
		return connector.SlackCall(c.http, req, out)
	}
	return connector.Do(c.http, req, c.p.ID, out)
}

// clamp returns v limited to [lo, hi], or def when v is zero.
func clamp(v, def, lo, hi int) int {
	if v == 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
