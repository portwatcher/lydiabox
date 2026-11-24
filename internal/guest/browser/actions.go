package browser

import (
	"context"
	"fmt"
	"strings"

	"github.com/portwatcher/lydiabox/internal/guest/x11"
)

// located is the result of the locate, focus, select, and reveal scripts.
type located struct {
	Found     bool     `json:"found"`
	Desc      string   `json:"desc"`
	Covered   string   `json:"covered"`
	Disabled  bool     `json:"disabled"`
	Password  bool     `json:"password"`
	Focused   bool     `json:"focused"`
	NotSelect bool     `json:"notSelect"`
	Missing   []string `json:"missing"`
	Options   []string `json:"options"`
	Selected  []string `json:"selected"`
	X         float64  `json:"x"`
	Y         float64  `json:"y"`
	SX        int      `json:"sx"`
	SY        int      `json:"sy"`
}

func checkRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref is required (for example e5 from browser_snapshot)")
	}
	return nil
}

// Click clicks element ref at its center with real mouse events, after
// scrolling it into view. A click that opens a new tab makes that tab
// current; a click that starts a navigation waits for it.
func (b *Browser) Click(ctx context.Context, ref string, double bool, button string) (string, error) {
	if err := checkRef(ref); err != nil {
		return "", err
	}
	button = orDefault(button, "left")
	if button != "left" && button != "middle" && button != "right" {
		return "", fmt.Errorf("button must be left, middle, or right")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var (
		p    *page
		loc  located
		prev []tab
	)
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		var err error
		if p, err = b.current(ctx); err != nil {
			return err
		}
		if prev, err = b.tabs(ctx); err != nil {
			return err
		}
		if err := p.eval(ctx, script("locate", locateBody, ref), &loc); err != nil {
			return err
		}
		if !loc.Found {
			return fmt.Errorf("%s: %w", ref, ErrNoRef)
		}
		count := 1
		if double {
			count = 2
		}
		return p.mouseClick(ctx, loc.X, loc.Y, button, count)
	})
	if err != nil {
		return "", err
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "click", X: loc.SX, Y: loc.SY, Button: button, Double: double, Tool: "browser_click"})
	var sb strings.Builder
	fmt.Fprintf(&sb, "Clicked %s %s", ref, loc.Desc)
	if loc.Disabled {
		sb.WriteString("\nNote: the element is disabled.")
	}
	if loc.Covered != "" {
		fmt.Fprintf(&sb, "\nNote: %s covers the element center; the click went to it.", loc.Covered)
	}
	p, opened := b.followNewTab(ctx, p, prev)
	if opened {
		sb.WriteString("\nThe click opened a new tab.")
	}
	sb.WriteString("\n" + b.settle(ctx, p))
	return sb.String(), nil
}

// followNewTab makes a tab that appeared since prev the current tab.
func (b *Browser) followNewTab(ctx context.Context, p *page, prev []tab) (*page, bool) {
	sleep(ctx, b.opts.Poll)
	ts, err := b.tabs(ctx)
	if err != nil {
		return p, false
	}
	seen := map[string]bool{}
	for _, t := range prev {
		seen[t.TargetID] = true
	}
	for i := len(ts) - 1; i >= 0; i-- {
		if !seen[ts[i].TargetID] {
			b.active = ts[i].TargetID
			return &page{b: b, tab: ts[i]}, true
		}
	}
	return p, false
}

// settle waits for a navigation that an action may have started and
// returns the page line.
func (b *Browser) settle(ctx context.Context, p *page) string {
	sleep(ctx, b.opts.Poll)
	loaded, err := b.waitLoad(ctx, p, b.opts.SettleTimeout)
	if err != nil {
		loaded = false
	}
	return b.describePage(ctx, p, loaded)
}

func (p *page) mouseClick(ctx context.Context, x, y float64, button string, count int) error {
	mask := map[string]int{"left": 1, "right": 2, "middle": 4}[button]
	if err := p.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil); err != nil {
		return err
	}
	for i := 1; i <= count; i++ {
		for _, typ := range []string{"mousePressed", "mouseReleased"} {
			ev := map[string]any{"type": typ, "x": x, "y": y, "button": button, "clickCount": i, "buttons": mask}
			if typ == "mouseReleased" {
				ev["buttons"] = 0
			}
			if err := p.call(ctx, "Input.dispatchMouseEvent", ev, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// Type focuses element ref (or keeps the focused element when ref is "")
// and inserts text at the end of its value, or replaces the value when
// clear is set. submit presses Enter afterwards.
func (b *Browser) Type(ctx context.Context, ref, text string, clear, submit bool) (string, error) {
	if text == "" && !clear && !submit {
		return "", fmt.Errorf("text is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var (
		p   *page
		loc located
	)
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		var err error
		if p, err = b.current(ctx); err != nil {
			return err
		}
		if err := p.eval(ctx, script("focus", focusBody, ref, clear), &loc); err != nil {
			return err
		}
		if !loc.Found {
			if ref == "" {
				return fmt.Errorf("no element has focus; pass a ref")
			}
			return fmt.Errorf("%s: %w", ref, ErrNoRef)
		}
		switch {
		case text != "":
			if err := p.call(ctx, "Input.insertText", map[string]any{"text": text}, nil); err != nil {
				return err
			}
		case clear:
			if err := p.pressKey(ctx, "Backspace"); err != nil {
				return err
			}
		}
		if submit {
			return p.pressKey(ctx, "Enter")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	shown := text
	if loc.Password {
		shown = strings.Repeat("•", len([]rune(text)))
	}
	if text != "" {
		b.opts.Desktop.Observe(x11.AgentAction{Action: "type", Text: shown, Tool: "browser_type"})
	}
	target := "the focused element"
	if ref != "" {
		target = ref + " " + loc.Desc
	}
	msg := fmt.Sprintf("Typed %d characters into %s", len([]rune(text)), target)
	if clear {
		msg = fmt.Sprintf("Replaced the value of %s with %d characters", target, len([]rune(text)))
	}
	if !loc.Focused {
		msg += "\nNote: the element did not take focus; the text may have gone elsewhere."
	}
	if submit {
		b.opts.Desktop.Observe(x11.AgentAction{Action: "key", Keys: "Enter", Tool: "browser_type"})
		return msg + " and pressed Enter\n" + b.settle(ctx, p), nil
	}
	return msg, nil
}

// Select chooses options of the <select> element ref by value or label.
func (b *Browser) Select(ctx context.Context, ref string, values []string) (string, error) {
	if err := checkRef(ref); err != nil {
		return "", err
	}
	if len(values) == 0 {
		return "", fmt.Errorf("at least one value is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var loc located
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		p, err := b.current(ctx)
		if err != nil {
			return err
		}
		if err := p.eval(ctx, script("select", selectBody, ref, values), &loc); err != nil {
			return err
		}
		switch {
		case !loc.Found:
			return fmt.Errorf("%s: %w", ref, ErrNoRef)
		case loc.NotSelect:
			return fmt.Errorf("%s %s is not a <select>; click it and then click the option", ref, loc.Desc)
		case len(loc.Missing) > 0:
			return fmt.Errorf("%s has no option %q; options: %s", ref, strings.Join(loc.Missing, ", "), strings.Join(loc.Options, " | "))
		case len(loc.Selected) == 0:
			return fmt.Errorf("%s accepts one option only; options: %s", ref, strings.Join(loc.Options, " | "))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "click", X: loc.SX, Y: loc.SY, Button: "left", Tool: "browser_select"})
	return fmt.Sprintf("Selected %s in %s %s", strings.Join(loc.Selected, ", "), ref, loc.Desc), nil
}

// Press presses a key or a key combination ("Enter", "ctrl+a",
// "shift+Tab") in the current tab, after focusing ref when it is set.
func (b *Browser) Press(ctx context.Context, keys, ref string) (string, error) {
	if _, err := parseCombo(keys); err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var p *page
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		var err error
		if p, err = b.current(ctx); err != nil {
			return err
		}
		if ref != "" {
			var loc located
			if err := p.eval(ctx, script("focus", focusBody, ref, false), &loc); err != nil {
				return err
			}
			if !loc.Found {
				return fmt.Errorf("%s: %w", ref, ErrNoRef)
			}
		}
		return p.pressKey(ctx, keys)
	})
	if err != nil {
		return "", err
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "key", Keys: keys, Tool: "browser_press"})
	return "Pressed " + keys + "\n" + b.settle(ctx, p), nil
}

func (p *page) pressKey(ctx context.Context, combo string) error {
	k, err := parseCombo(combo)
	if err != nil {
		return err
	}
	down := map[string]any{"type": "rawKeyDown", "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk,
		"nativeVirtualKeyCode": k.vk, "modifiers": k.mods}
	if len(k.commands) > 0 {
		down["commands"] = k.commands
	}
	if k.text != "" {
		down["type"] = "keyDown"
		down["text"] = k.text
		down["unmodifiedText"] = k.text
	}
	if err := p.call(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
		return err
	}
	return p.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": k.key, "code": k.code,
		"windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk, "modifiers": k.mods}, nil)
}

// Scroll scrolls element ref into view, or scrolls the page by (dx, dy)
// CSS pixels with the mouse wheel at the viewport center (dy > 0 is
// down).
func (b *Browser) Scroll(ctx context.Context, ref string, dx, dy int) (string, error) {
	if ref == "" && dx == 0 && dy == 0 {
		return "", fmt.Errorf("pass a ref or a non-zero dx or dy")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var (
		p   *page
		loc located
		vp  viewport
	)
	err := b.opts.Desktop.Act(ctx, x11.AgentAction{}, func() error {
		var err error
		if p, err = b.current(ctx); err != nil {
			return err
		}
		if ref != "" {
			if err := p.eval(ctx, script("reveal", revealBody, ref), &loc); err != nil {
				return err
			}
			if !loc.Found {
				return fmt.Errorf("%s: %w", ref, ErrNoRef)
			}
			return nil
		}
		if err := p.eval(ctx, script("viewport", viewportBody), &vp); err != nil {
			return err
		}
		return p.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel",
			"x": vp.W / 2, "y": vp.H / 2, "deltaX": dx, "deltaY": dy}, nil)
	})
	if err != nil {
		return "", err
	}
	b.opts.Desktop.Observe(x11.AgentAction{Action: "scroll", X: loc.SX, Y: loc.SY, DX: wheelClicks(dx), DY: wheelClicks(dy), Tool: "browser_scroll"})
	sleep(ctx, 2*b.opts.Poll) // smooth scrolling
	if err := p.eval(ctx, script("viewport", viewportBody), &vp); err != nil {
		return "", err
	}
	pos := fmt.Sprintf("Scroll position: y=%d of %d, viewport %dx%d", vp.Y, vp.Height, vp.W, vp.H)
	if ref != "" {
		return fmt.Sprintf("Scrolled %s %s into view\n%s", ref, loc.Desc, pos), nil
	}
	return fmt.Sprintf("Scrolled by dx=%d dy=%d\n%s", dx, dy, pos), nil
}

// wheelClicks converts pixels to the wheel clicks of the agent_action
// event (about 100 pixels per click).
func wheelClicks(px int) int {
	switch {
	case px == 0:
		return 0
	case px > 0:
		return max(1, px/100)
	}
	return min(-1, px/100)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
