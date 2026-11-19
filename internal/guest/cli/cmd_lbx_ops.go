package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// lbxOp maps one lbx operation to an MCP tool call.
type lbxOp struct {
	usage   string
	summary string
	bools   []string // boolean flags
	values  []string // flags that take a value
	min     int      // positional arguments
	max     int      // -1: any number
	build   func(in *parsed) (string, map[string]any, error)
}

// parsed is the parsed command line of an operation.
type parsed struct {
	pos   []string
	flags map[string]string
	stdin io.Reader
}

func (p *parsed) flag(name string) bool { _, ok := p.flags[name]; return ok }

func (p *parsed) value(name string) string { return p.flags[name] }

func (p *parsed) intFlag(name string) (int, bool, error) {
	v, ok := p.flags[name]
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false, fmt.Errorf("--%s needs a whole number", name)
	}
	return n, true, nil
}

// Flags of every operation.
var (
	globalBools  = []string{"json"}
	globalValues = []string{"out"}
)

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// parseArgs splits flags (--name, --name value, --name=value) from
// positional arguments. "-" and negative numbers are positional; "--"
// ends the flags.
func parseArgs(args []string, op lbxOp) (*parsed, error) {
	p := &parsed{flags: map[string]string{}}
	bools := append(slices.Clone(op.bools), globalBools...)
	values := append(slices.Clone(op.values), globalValues...)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.pos = append(p.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" || isNumber(a) {
			p.pos = append(p.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case slices.Contains(bools, name):
			if hasVal {
				return nil, fmt.Errorf("--%s takes no value", name)
			}
			p.flags[name] = ""
		case slices.Contains(values, name):
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
			p.flags[name] = val
		default:
			return nil, fmt.Errorf("unknown flag %s", a)
		}
	}
	if len(p.pos) < op.min || (op.max >= 0 && len(p.pos) > op.max) {
		return nil, errors.New("wrong number of arguments")
	}
	return p, nil
}

func atoi(name, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, not %q", name, s)
	}
	return n, nil
}

// ref accepts "e5", "[e5]", or "-" (no ref: the focused element).
func ref(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "[]")
	if s == "-" {
		return ""
	}
	return s
}

func args(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func fixed(tool string, a map[string]any) func(*parsed) (string, map[string]any, error) {
	return func(*parsed) (string, map[string]any, error) { return tool, a, nil }
}

func typeOp(clear bool) func(in *parsed) (string, map[string]any, error) {
	return func(in *parsed) (string, map[string]any, error) {
		return "browser_type", args("ref", ref(in.pos[0]), "text", strings.Join(in.pos[1:], " "),
			"clear", clear || in.flag("clear"), "submit", in.flag("submit")), nil
	}
}

var browserOps = map[string]lbxOp{
	"open": {usage: "URL [--new-tab]", summary: "open a URL and wait for the load", bools: []string{"new-tab"}, min: 1, max: 1,
		build: func(in *parsed) (string, map[string]any, error) {
			return "browser_open", args("url", in.pos[0], "new_tab", in.flag("new-tab")), nil
		}},
	"new": {usage: "[URL]", summary: "open a new tab", max: 1, build: func(in *parsed) (string, map[string]any, error) {
		u := "about:blank"
		if len(in.pos) == 1 {
			u = in.pos[0]
		}
		return "browser_open", args("url", u, "new_tab", true), nil
	}},
	"back":    {summary: "go back", build: fixed("browser_back", args())},
	"forward": {summary: "go forward", build: fixed("browser_forward", args())},
	"tabs":    {summary: "list tabs (* is current)", build: fixed("browser_tabs", args())},
	"tab": {usage: "N", summary: "switch to tab N", min: 1, max: 1, build: func(in *parsed) (string, map[string]any, error) {
		n, err := atoi("N", in.pos[0])
		return "browser_tabs", args("action", "select", "tab", n), err
	}},
	"close": {usage: "[N]", summary: "close tab N (default: current)", max: 1, build: func(in *parsed) (string, map[string]any, error) {
		a := args("action", "close")
		if len(in.pos) == 1 {
			n, err := atoi("N", in.pos[0])
			if err != nil {
				return "", nil, err
			}
			a["tab"] = n
		}
		return "browser_tabs", a, nil
	}},
	"snapshot": {usage: "[--limit N]", summary: "headings and interactive elements with refs [e5]", values: []string{"limit"},
		build: func(in *parsed) (string, map[string]any, error) {
			n, _, err := in.intFlag("limit")
			return "browser_snapshot", args("limit", n), err
		}},
	"click": {usage: "REF [--double] [--button B]", summary: "click an element", bools: []string{"double"},
		values: []string{"button"}, min: 1, max: 1, build: func(in *parsed) (string, map[string]any, error) {
			return "browser_click", args("ref", ref(in.pos[0]), "double", in.flag("double"), "button", in.value("button")), nil
		}},
	"type": {usage: "REF|- TEXT [--clear] [--submit]", summary: "type into an element (- is the focused one)",
		bools: []string{"clear", "submit"}, min: 2, max: -1, build: typeOp(false)},
	"fill": {usage: "REF TEXT [--submit]", summary: "replace the value of a field", bools: []string{"submit"}, min: 2, max: -1,
		build: typeOp(true)},
	"select": {usage: "REF VALUE...", summary: "choose <select> options by value or label", min: 2, max: -1,
		build: func(in *parsed) (string, map[string]any, error) {
			return "browser_select", args("ref", ref(in.pos[0]), "values", in.pos[1:]), nil
		}},
	"press": {usage: "KEY [--ref REF]", summary: "press a key: Enter, Escape, Tab, ArrowDown, ctrl+a", values: []string{"ref"},
		min: 1, max: 1, build: func(in *parsed) (string, map[string]any, error) {
			return "browser_press", args("key", in.pos[0], "ref", ref(in.value("ref"))), nil
		}},
	"scroll": {usage: "REF | down|up|left|right [PIXELS]", summary: "scroll an element into view, or the page", min: 1, max: 2,
		build: func(in *parsed) (string, map[string]any, error) {
			dir := in.pos[0]
			px := 600
			if len(in.pos) == 2 {
				n, err := atoi("PIXELS", in.pos[1])
				if err != nil {
					return "", nil, err
				}
				px = n
			}
			switch dir {
			case "down":
				return "browser_scroll", args("dy", px), nil
			case "up":
				return "browser_scroll", args("dy", -px), nil
			case "right":
				return "browser_scroll", args("dx", px), nil
			case "left":
				return "browser_scroll", args("dx", -px), nil
			}
			if len(in.pos) == 2 {
				return "", nil, errors.New("use REF alone, or a direction and pixels")
			}
			return "browser_scroll", args("ref", ref(dir)), nil
		}},
	"text": {usage: "[REF] [--max N]", summary: "read the page text (or of one element)", values: []string{"max"}, max: 1,
		build: func(in *parsed) (string, map[string]any, error) {
			n, _, err := in.intFlag("max")
			a := args("max_chars", n)
			if len(in.pos) == 1 {
				a["ref"] = ref(in.pos[0])
			}
			return "browser_text", a, err
		}},
	"eval": {usage: "JS | -", summary: "evaluate JavaScript in the page (- reads stdin)", min: 1, max: -1,
		build: func(in *parsed) (string, map[string]any, error) {
			js := strings.Join(in.pos, " ")
			if js == "-" {
				b, err := io.ReadAll(in.stdin)
				if err != nil {
					return "", nil, err
				}
				js = string(b)
			}
			return "browser_eval", args("expression", js), nil
		}},
	"screenshot": {usage: "[--full] [--out FILE]", summary: "save a PNG of the page and print its path", bools: []string{"full"},
		build: func(in *parsed) (string, map[string]any, error) {
			return "browser_screenshot", args("full_page", in.flag("full")), nil
		}},
	"wait": {usage: "load|selector CSS|text TEXT|url PART [--timeout S]", summary: "wait for a page condition",
		values: []string{"timeout"}, min: 1, max: -1, build: func(in *parsed) (string, map[string]any, error) {
			n, _, err := in.intFlag("timeout")
			return "browser_wait", args("for", in.pos[0], "value", strings.Join(in.pos[1:], " "), "timeout_seconds", n), err
		}},
}

func point(in *parsed, i int) (int, int, error) {
	x, err := atoi("X", in.pos[i])
	if err != nil {
		return 0, 0, err
	}
	y, err := atoi("Y", in.pos[i+1])
	return x, y, err
}

var computerOps = map[string]lbxOp{
	"screenshot": {usage: "[--jpeg] [--out FILE]", summary: "save a screenshot of the desktop and print its path",
		bools: []string{"jpeg"}, build: func(in *parsed) (string, map[string]any, error) {
			f := "png"
			if in.flag("jpeg") {
				f = "jpeg"
			}
			return "computer_screenshot", args("format", f), nil
		}},
	"click": {usage: "X Y [--button B] [--double]", summary: "click at a screen point", bools: []string{"double"},
		values: []string{"button"}, min: 2, max: 2, build: func(in *parsed) (string, map[string]any, error) {
			x, y, err := point(in, 0)
			a := args("x", x, "y", y, "double", in.flag("double"))
			if b := in.value("button"); b != "" {
				a["button"] = b
			}
			return "computer_mouse_click", a, err
		}},
	"move": {usage: "X Y", summary: "move the pointer", min: 2, max: 2, build: func(in *parsed) (string, map[string]any, error) {
		x, y, err := point(in, 0)
		return "computer_mouse_move", args("x", x, "y", y), err
	}},
	"drag": {usage: "FROM_X FROM_Y X Y [--button B]", summary: "drag with a button held", values: []string{"button"}, min: 4, max: 4,
		build: func(in *parsed) (string, map[string]any, error) {
			fx, fy, err := point(in, 0)
			if err != nil {
				return "", nil, err
			}
			x, y, err := point(in, 2)
			a := args("from_x", fx, "from_y", fy, "x", x, "y", y)
			if b := in.value("button"); b != "" {
				a["button"] = b
			}
			return "computer_mouse_drag", a, err
		}},
	"type": {usage: "TEXT", summary: "type text with the keyboard", min: 1, max: -1, build: func(in *parsed) (string, map[string]any, error) {
		return "computer_keyboard", args("text", strings.Join(in.pos, " ")), nil
	}},
	"key": {usage: "COMBO", summary: "press keys: Return, ctrl+l, alt+F4", min: 1, max: 1, build: func(in *parsed) (string, map[string]any, error) {
		return "computer_keyboard", args("key_combo", in.pos[0]), nil
	}},
	"scroll": {usage: "X Y [down|up|left|right] [CLICKS]", summary: "scroll with the wheel at a point (default down 3)", min: 2, max: 4,
		build: func(in *parsed) (string, map[string]any, error) {
			x, y, err := point(in, 0)
			if err != nil {
				return "", nil, err
			}
			dir, n := "down", 3
			if len(in.pos) > 2 {
				dir = in.pos[2]
			}
			if len(in.pos) > 3 {
				if n, err = atoi("CLICKS", in.pos[3]); err != nil {
					return "", nil, err
				}
			}
			a := args("x", x, "y", y)
			switch dir {
			case "down":
				a["dy"] = n
			case "up":
				a["dy"] = -n
			case "right":
				a["dx"] = n
			case "left":
				a["dx"] = -n
			default:
				return "", nil, fmt.Errorf("direction must be down, up, left, or right")
			}
			return "computer_scroll", a, nil
		}},
	"launch": {usage: "chrome|blender|terminal", summary: "start a desktop application", min: 1, max: 1,
		build: func(in *parsed) (string, map[string]any, error) {
			return "desktop_launch_app", args("app", in.pos[0]), nil
		}},
}

func opsUsage(group string, ops map[string]lbxOp) string {
	names := make([]string, 0, len(ops))
	for n := range ops {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	fmt.Fprintf(&sb, "Usage: lbx %s <op> [args]\n\n", group)
	for _, n := range names {
		o := ops[n]
		fmt.Fprintf(&sb, "  %-44s %s\n", strings.TrimSpace(n+" "+o.usage), o.summary)
	}
	sb.WriteString("\nB is left, middle, or right.")
	if group == "browser" {
		sb.WriteString("\nWorkflow: open a URL, run snapshot to get refs such as [e5], then click/type/select by ref.\n" +
			"Take a new snapshot after the page changes. Add --json for the raw result.\n")
	} else {
		sb.WriteString("\nCoordinates are pixels of the 1920x1080 desktop; take a screenshot before acting.\n")
	}
	return sb.String()
}
