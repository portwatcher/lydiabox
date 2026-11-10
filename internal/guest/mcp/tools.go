package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lydiabox/lydiabox/internal/guest/x11"
)

// ToolInfo is a tools/list entry.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Content is one MCP content item (text or image).
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ToolResult is the tools/call result.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type tool struct {
	ToolInfo
	run func(ctx context.Context, args json.RawMessage) (*ToolResult, error)
}

func textResult(format string, args ...any) *ToolResult {
	return &ToolResult{Content: []Content{{Type: "text", Text: fmt.Sprintf(format, args...)}}}
}

// errorResult reports a tool failure. For x11.ErrTakeover the text is the
// structured takeover notice the agent should act on.
func errorResult(err error) *ToolResult {
	msg := err.Error()
	if errors.Is(err, x11.ErrTakeover) {
		msg = x11.TakeoverMessage
	}
	return &ToolResult{Content: []Content{{Type: "text", Text: msg}}, IsError: true}
}

// decode strictly decodes tool arguments.
func decode(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	intProp    = func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	strProp    = func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	buttonProp = map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}, "default": "left"}
)

func (s *Server) toolSet() []tool {
	return []tool{
		{ToolInfo{"computer_screenshot", "Capture the 1920x1080 guest desktop.", schema(map[string]any{
			"format":  map[string]any{"type": "string", "enum": []string{"png", "jpeg"}, "default": "png"},
			"quality": intProp("JPEG quality 1-100 (default 80)"),
		})}, s.screenshot},
		{ToolInfo{"computer_mouse_click", "Move the pointer to (x, y) and click.", schema(map[string]any{
			"x": intProp("X pixel"), "y": intProp("Y pixel"), "button": buttonProp,
			"double": map[string]any{"type": "boolean", "default": false},
		}, "x", "y")}, s.click},
		{ToolInfo{"computer_mouse_move", "Move the pointer to (x, y).", schema(map[string]any{
			"x": intProp("X pixel"), "y": intProp("Y pixel"),
		}, "x", "y")}, s.move},
		{ToolInfo{"computer_mouse_drag", "Press a button at (from_x, from_y), drag to (x, y), and release.", schema(map[string]any{
			"from_x": intProp("start X"), "from_y": intProp("start Y"), "x": intProp("end X"), "y": intProp("end Y"),
			"button": buttonProp,
		}, "from_x", "from_y", "x", "y")}, s.drag},
		{ToolInfo{"computer_scroll", "Scroll at (x, y): dy > 0 scrolls down, dx > 0 right, in wheel clicks.", schema(map[string]any{
			"x": intProp("X pixel"), "y": intProp("Y pixel"), "dx": intProp("horizontal clicks"), "dy": intProp("vertical clicks"),
		}, "x", "y")}, s.scroll},
		{ToolInfo{"computer_keyboard", "Type text, press a key combo (for example \"ctrl+l\", \"Return\", \"F5\"), or both (text first).", schema(map[string]any{
			"text": strProp("UTF-8 text to type"), "key_combo": strProp("keys joined with +"),
		})}, s.keyboard},
		{ToolInfo{"desktop_launch_app", "Launch a desktop application.", schema(map[string]any{
			"app": map[string]any{"type": "string", "enum": []string{"chrome", "blender", "terminal"}},
		}, "app")}, s.launch},
		{ToolInfo{"browser_cdp_endpoint", "Return the Chrome DevTools Protocol WebSocket URL (port 9222), starting Chrome if needed.",
			schema(map[string]any{})}, s.cdp},
		{ToolInfo{"blender_run_script", "Run Python code in Blender (background mode) and return its output.", schema(map[string]any{
			"code":            strProp("Python source using bpy"),
			"blend_file":      strProp("optional .blend file to open first"),
			"timeout_seconds": intProp("default 300, maximum 3600"),
		}, "code")}, s.blender},
	}
}

func (s *Server) screenshot(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Format  string `json:"format"`
		Quality int    `json:"quality"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	img, err := s.desktop.Screenshot(ctx, "computer_screenshot")
	if err != nil {
		return nil, err
	}
	var (
		data []byte
		mime string
	)
	switch a.Format {
	case "", "png":
		data, err = x11.EncodePNG(img)
		mime = "image/png"
	case "jpeg", "jpg":
		data, err = x11.EncodeJPEG(img, a.Quality)
		mime = "image/jpeg"
	default:
		return nil, fmt.Errorf("format must be png or jpeg")
	}
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	return &ToolResult{Content: []Content{
		{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
		{Type: "text", Text: fmt.Sprintf("Screenshot %dx%d", b.Dx(), b.Dy())},
	}}, nil
}

type point struct {
	X *int `json:"x"`
	Y *int `json:"y"`
}

func (p point) get() (int, int, error) {
	if p.X == nil || p.Y == nil {
		return 0, 0, fmt.Errorf("x and y are required")
	}
	return *p.X, *p.Y, nil
}

func (s *Server) click(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		point
		Button string `json:"button"`
		Double bool   `json:"double"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	x, y, err := a.get()
	if err != nil {
		return nil, err
	}
	if err := s.desktop.Click(ctx, "computer_mouse_click", x, y, a.Button, a.Double); err != nil {
		return nil, err
	}
	return textResult("Clicked %s at (%d, %d)", orDefault(a.Button, "left"), x, y), nil
}

func (s *Server) move(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a point
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	x, y, err := a.get()
	if err != nil {
		return nil, err
	}
	if err := s.desktop.MoveTo(ctx, "computer_mouse_move", x, y); err != nil {
		return nil, err
	}
	return textResult("Moved pointer to (%d, %d)", x, y), nil
}

func (s *Server) drag(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		point
		FromX  *int   `json:"from_x"`
		FromY  *int   `json:"from_y"`
		Button string `json:"button"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	x, y, err := a.get()
	if err != nil || a.FromX == nil || a.FromY == nil {
		return nil, fmt.Errorf("from_x, from_y, x, and y are required")
	}
	if err := s.desktop.Drag(ctx, "computer_mouse_drag", *a.FromX, *a.FromY, x, y, a.Button); err != nil {
		return nil, err
	}
	return textResult("Dragged from (%d, %d) to (%d, %d)", *a.FromX, *a.FromY, x, y), nil
}

func (s *Server) scroll(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		point
		DX int `json:"dx"`
		DY int `json:"dy"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	x, y, err := a.get()
	if err != nil {
		return nil, err
	}
	if a.DX == 0 && a.DY == 0 {
		return nil, fmt.Errorf("dx or dy must be non-zero")
	}
	if err := s.desktop.Scroll(ctx, "computer_scroll", x, y, a.DX, a.DY); err != nil {
		return nil, err
	}
	return textResult("Scrolled dx=%d dy=%d at (%d, %d)", a.DX, a.DY, x, y), nil
}

func (s *Server) keyboard(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Text     string `json:"text"`
		KeyCombo string `json:"key_combo"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.Text == "" && a.KeyCombo == "" {
		return nil, fmt.Errorf("text or key_combo is required")
	}
	var done []string
	if a.Text != "" {
		if err := s.desktop.TypeText(ctx, "computer_keyboard", a.Text); err != nil {
			return nil, err
		}
		done = append(done, fmt.Sprintf("typed %d characters", len([]rune(a.Text))))
	}
	if a.KeyCombo != "" {
		if err := s.desktop.KeyCombo(ctx, "computer_keyboard", a.KeyCombo); err != nil {
			return nil, err
		}
		done = append(done, "pressed "+a.KeyCombo)
	}
	return textResult("%s", strings.Join(done, ", ")), nil
}

func (s *Server) launch(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		App string `json:"app"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if err := s.apps.Launch(ctx, a.App); err != nil {
		return nil, err
	}
	s.desktop.Launched("desktop_launch_app", a.App)
	return textResult("Launched %s", a.App), nil
}

func (s *Server) cdp(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	if err := decode(args, &struct{}{}); err != nil {
		return nil, err
	}
	ws, err := s.apps.CDPEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	return textResult("%s", ws), nil
}

func (s *Server) blender(ctx context.Context, args json.RawMessage) (*ToolResult, error) {
	var a struct {
		Code           string `json:"code"`
		BlendFile      string `json:"blend_file"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if strings.TrimSpace(a.Code) == "" {
		return nil, fmt.Errorf("code is required")
	}
	res, err := s.apps.RunBlender(ctx, BlenderRequest{Code: a.Code, BlendFile: a.BlendFile, TimeoutSeconds: a.TimeoutSeconds})
	if err != nil {
		return nil, err
	}
	out := &ToolResult{Content: []Content{{Type: "text", Text: res.Output}}}
	if res.ExitCode != 0 {
		out.IsError = true
		out.Content = append(out.Content, Content{Type: "text", Text: fmt.Sprintf("blender exited with code %d", res.ExitCode)})
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
