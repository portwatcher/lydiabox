package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lbx is the shell form of the guest MCP tools, for every harness: it
// calls the tools of the running guest daemon (http://127.0.0.1:8910/mcp),
// so the CLI and the MCP tools share one browser session, the same
// element refs, the takeover rules, and the agent_action events.
//
//	lbx browser <op> ...    (also lydiabox-guest browser <op> ...)
//	lbx computer <op> ...   (also lydiabox-guest computer <op> ...)
//	lbx tools               list the MCP tools
//	lbx call <tool> [json]  call any MCP tool

func init() {
	Register(Command{Name: "lbx", Summary: "Browser and computer use from the shell (also as lbx)", Run: runLbx})
	Register(Command{Name: "browser", Summary: "Drive the desktop Chrome (lbx browser help)", Run: func(ctx context.Context, env Env, args []string) error {
		return runLbx(ctx, env, append([]string{"browser"}, args...))
	}})
	Register(Command{Name: "computer", Summary: "Use the desktop: screenshot, mouse, keyboard (lbx computer help)", Run: func(ctx context.Context, env Env, args []string) error {
		return runLbx(ctx, env, append([]string{"computer"}, args...))
	}})
}

// errReported is an error whose message the command already printed.
var errReported = errors.New("reported")

// MCPURLEnv overrides the guest MCP endpoint of lbx.
const MCPURLEnv = "LYDIABOX_MCP_URL"

const defaultMCPURL = "http://127.0.0.1:8910/mcp"

func runLbx(ctx context.Context, env Env, args []string) error {
	err := lbx(ctx, env, args)
	if err != nil && !errors.Is(err, errReported) {
		fmt.Fprintf(env.Stderr, "lbx: %v\n", err)
		return errReported
	}
	return err
}

func lbx(ctx context.Context, env Env, args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		fmt.Fprint(env.Stdout, lbxUsage)
		if len(args) == 0 {
			return errReported
		}
		return nil
	}
	group, rest := args[0], args[1:]
	switch group {
	case "browser", "computer":
		ops := browserOps
		if group == "computer" {
			ops = computerOps
		}
		if len(rest) == 0 || isHelp(rest[0]) {
			fmt.Fprint(env.Stdout, opsUsage(group, ops))
			return nil
		}
		op, ok := ops[rest[0]]
		if !ok {
			fmt.Fprintf(env.Stderr, "lbx %s: unknown operation %q\n\n%s", group, rest[0], opsUsage(group, ops))
			return errReported
		}
		in, err := parseArgs(rest[1:], op)
		if err != nil {
			return fmt.Errorf("%s %s: %w\nusage: lbx %s %s %s", group, rest[0], err, group, rest[0], op.usage)
		}
		in.stdin = env.Stdin
		tool, toolArgs, err := op.build(in)
		if err != nil {
			return fmt.Errorf("%s %s: %w\nusage: lbx %s %s %s", group, rest[0], err, group, rest[0], op.usage)
		}
		return callAndPrint(ctx, env, tool, toolArgs, in)
	case "tools":
		return listTools(ctx, env)
	case "call":
		if len(rest) == 0 {
			return errors.New("usage: lbx call <tool> [json arguments | -]")
		}
		raw := "{}"
		if len(rest) > 1 {
			raw = rest[1]
		}
		if raw == "-" {
			b, err := io.ReadAll(env.Stdin)
			if err != nil {
				return err
			}
			raw = string(b)
		}
		var a map[string]any
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return fmt.Errorf("arguments must be a JSON object: %w", err)
		}
		return callAndPrint(ctx, env, rest[0], a, &parsed{})
	}
	fmt.Fprintf(env.Stderr, "lbx: unknown command %q\n\n%s", group, lbxUsage)
	return errReported
}

func isHelp(s string) bool { return s == "help" || s == "-h" || s == "--help" }

func mcpURL() string {
	if u := os.Getenv(MCPURLEnv); u != "" {
		return u
	}
	return defaultMCPURL
}

type toolContent struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError"`
}

// mcpCall posts one JSON-RPC request to the guest MCP server.
func mcpCall(ctx context.Context, method string, params any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("the LydiaBox guest daemon is not reachable at %s: %w", mcpURL(), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d: %s", mcpURL(), resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("bad response from %s: %w", mcpURL(), err)
	}
	if r.Error != nil {
		return errors.New(r.Error.Message)
	}
	return json.Unmarshal(r.Result, out)
}

func callAndPrint(ctx context.Context, env Env, tool string, args map[string]any, in *parsed) error {
	var res toolResult
	if err := mcpCall(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &res); err != nil {
		return err
	}
	if in.flag("json") {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	w := env.Stdout
	if res.IsError {
		w = env.Stderr
	}
	for _, c := range res.Content {
		switch c.Type {
		case "image":
			path, err := saveImage(c, in.value("out"))
			if err != nil {
				return err
			}
			fmt.Fprintln(w, path)
		default:
			fmt.Fprintln(w, c.Text)
		}
	}
	if res.IsError {
		return errReported
	}
	return nil
}

// saveImage writes an image result to out, or to a new file in the
// temporary directory, and returns the absolute path.
func saveImage(c toolContent, out string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(c.Data)
	if err != nil {
		return "", fmt.Errorf("decode image: %w", err)
	}
	if out == "" {
		ext := ".png"
		if c.MimeType == "image/jpeg" {
			ext = ".jpg"
		}
		out = filepath.Join(os.TempDir(), "lbx-"+time.Now().Format("20060102-150405.000")+ext)
	}
	if out, err = filepath.Abs(out); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return "", err
	}
	return out, nil
}

func listTools(ctx context.Context, env Env) error {
	var res struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := mcpCall(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return err
	}
	for _, t := range res.Tools {
		fmt.Fprintf(env.Stdout, "%-22s %s\n", t.Name, t.Description)
	}
	return nil
}

const lbxUsage = `lbx: browser and computer use on this LydiaBox desktop. The user watches
the same screen and can take it over; actions wait while they do.

  lbx browser <op> ...    drive the desktop Chrome by element refs (lbx browser help)
  lbx computer <op> ...   screenshot, mouse, and keyboard on the desktop (lbx computer help)
  lbx tools               list the MCP tools of the guest
  lbx call <tool> [json]  call any MCP tool ("-" reads the JSON from stdin)

Add --json to print the raw tool result.
`
