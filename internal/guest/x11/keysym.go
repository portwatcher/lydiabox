package x11

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// X11 keysyms used by the mapping tables (X11/keysymdef.h).
const (
	XKBackSpace uint32 = 0xff08
	XKTab       uint32 = 0xff09
	XKReturn    uint32 = 0xff0d
	XKPause     uint32 = 0xff13
	XKScrollLk  uint32 = 0xff14
	XKEscape    uint32 = 0xff1b
	XKHome      uint32 = 0xff50
	XKLeft      uint32 = 0xff51
	XKUp        uint32 = 0xff52
	XKRight     uint32 = 0xff53
	XKDown      uint32 = 0xff54
	XKPageUp    uint32 = 0xff55
	XKPageDown  uint32 = 0xff56
	XKEnd       uint32 = 0xff57
	XKPrint     uint32 = 0xff61
	XKInsert    uint32 = 0xff63
	XKMenu      uint32 = 0xff67
	XKNumLock   uint32 = 0xff7f
	XKKPEnter   uint32 = 0xff8d
	XKF1        uint32 = 0xffbe
	XKShiftL    uint32 = 0xffe1
	XKShiftR    uint32 = 0xffe2
	XKControlL  uint32 = 0xffe3
	XKControlR  uint32 = 0xffe4
	XKCapsLock  uint32 = 0xffe5
	XKAltL      uint32 = 0xffe9
	XKAltR      uint32 = 0xffea
	XKSuperL    uint32 = 0xffeb
	XKSuperR    uint32 = 0xffec
	XKDelete    uint32 = 0xffff
	XKSpace     uint32 = 0x0020
)

// KeysymForRune returns the keysym that types r: Latin-1 characters map to
// themselves, other Unicode characters to 0x01000000 + code point.
func KeysymForRune(r rune) (uint32, bool) {
	switch {
	case r == '\n' || r == '\r':
		return XKReturn, true
	case r == '\t':
		return XKTab, true
	case r == '\b':
		return XKBackSpace, true
	case r == 0x1b:
		return XKEscape, true
	case r < 0x20 || r == 0x7f || r == utf8.RuneError:
		return 0, false
	case r < 0x7f || (r >= 0xa0 && r <= 0xff):
		return uint32(r), true
	case r > 0x10ffff:
		return 0, false
	default:
		return 0x01000000 | uint32(r), true
	}
}

// domNamedKeys maps DOM KeyboardEvent.key values of non-printing keys.
var domNamedKeys = map[string]uint32{
	"Enter": XKReturn, "Tab": XKTab, "Backspace": XKBackSpace, "Escape": XKEscape,
	"Delete": XKDelete, "Insert": XKInsert, "Home": XKHome, "End": XKEnd,
	"PageUp": XKPageUp, "PageDown": XKPageDown, "ArrowLeft": XKLeft, "ArrowRight": XKRight,
	"ArrowUp": XKUp, "ArrowDown": XKDown, "CapsLock": XKCapsLock, "NumLock": XKNumLock,
	"ScrollLock": XKScrollLk, "Pause": XKPause, "PrintScreen": XKPrint, "ContextMenu": XKMenu,
	"Shift": XKShiftL, "Control": XKControlL, "Alt": XKAltL, "AltGraph": XKAltR,
	"Meta": XKSuperL, "OS": XKSuperL, "Super": XKSuperL,
}

// domCodes maps DOM KeyboardEvent.code values (physical keys) to the
// unshifted keysym of a US layout. Held modifiers then apply in the guest,
// which keeps shortcuts such as Ctrl+Shift+T working.
var domCodes = map[string]uint32{
	"ShiftLeft": XKShiftL, "ShiftRight": XKShiftR, "ControlLeft": XKControlL, "ControlRight": XKControlR,
	"AltLeft": XKAltL, "AltRight": XKAltR, "MetaLeft": XKSuperL, "MetaRight": XKSuperR,
	"OSLeft": XKSuperL, "OSRight": XKSuperR,
	"Enter": XKReturn, "NumpadEnter": XKKPEnter, "Tab": XKTab, "Backspace": XKBackSpace,
	"Escape": XKEscape, "Space": XKSpace, "Delete": XKDelete, "Insert": XKInsert,
	"Home": XKHome, "End": XKEnd, "PageUp": XKPageUp, "PageDown": XKPageDown,
	"ArrowLeft": XKLeft, "ArrowRight": XKRight, "ArrowUp": XKUp, "ArrowDown": XKDown,
	"CapsLock": XKCapsLock, "NumLock": XKNumLock, "ScrollLock": XKScrollLk, "Pause": XKPause,
	"PrintScreen": XKPrint, "ContextMenu": XKMenu,
	"Minus": '-', "Equal": '=', "BracketLeft": '[', "BracketRight": ']', "Backslash": '\\',
	"Semicolon": ';', "Quote": '\'', "Backquote": '`', "Comma": ',', "Period": '.', "Slash": '/',
	"IntlBackslash": '\\',
}

// KeysymForDOM maps a DOM keyboard event to a keysym. The physical code is
// preferred so modifiers behave like a real keyboard; key is the fallback
// for codes outside the table (other layouts, IME output).
func KeysymForDOM(code, key string) (uint32, error) {
	if ks, ok := domCodes[code]; ok {
		return ks, nil
	}
	switch {
	case len(code) == 4 && strings.HasPrefix(code, "Key") && code[3] >= 'A' && code[3] <= 'Z':
		return uint32(code[3] - 'A' + 'a'), nil
	case len(code) == 6 && strings.HasPrefix(code, "Digit") && code[5] >= '0' && code[5] <= '9':
		return uint32(code[5]), nil
	case strings.HasPrefix(code, "F"):
		var n int
		if _, err := fmt.Sscanf(code, "F%d", &n); err == nil && n >= 1 && n <= 24 && fmt.Sprintf("F%d", n) == code {
			return XKF1 + uint32(n-1), nil
		}
	}
	if ks, ok := domNamedKeys[key]; ok {
		return ks, nil
	}
	if utf8.RuneCountInString(key) == 1 {
		r, _ := utf8.DecodeRuneInString(key)
		if ks, ok := KeysymForRune(r); ok {
			return ks, nil
		}
	}
	return 0, fmt.Errorf("x11: no keysym for code %q key %q", code, key)
}

// comboNames maps the key names accepted in key combos ("ctrl+shift+t").
var comboNames = map[string]uint32{
	"ctrl": XKControlL, "control": XKControlL, "shift": XKShiftL, "alt": XKAltL, "option": XKAltL,
	"super": XKSuperL, "meta": XKSuperL, "cmd": XKSuperL, "win": XKSuperL,
	"enter": XKReturn, "return": XKReturn, "tab": XKTab, "esc": XKEscape, "escape": XKEscape,
	"backspace": XKBackSpace, "delete": XKDelete, "del": XKDelete, "insert": XKInsert,
	"home": XKHome, "end": XKEnd, "pageup": XKPageUp, "pagedown": XKPageDown, "pgup": XKPageUp,
	"pgdn": XKPageDown, "up": XKUp, "down": XKDown, "left": XKLeft, "right": XKRight,
	"space": XKSpace, "capslock": XKCapsLock, "print": XKPrint, "menu": XKMenu,
	"plus": '+', "minus": '-',
}

// ParseCombo parses "ctrl+shift+t", "Return", or "F5" into keysyms in press
// order. Names follow xdotool conventions and are case-insensitive, except
// that a single character is used as typed.
func ParseCombo(combo string) ([]uint32, error) {
	combo = strings.TrimSpace(combo)
	if combo == "" {
		return nil, fmt.Errorf("x11: empty key combo")
	}
	var parts []string
	if combo == "+" {
		parts = []string{"+"}
	} else {
		parts = strings.Split(combo, "+")
	}
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("x11: invalid key combo %q", combo)
		}
		if utf8.RuneCountInString(p) == 1 {
			r, _ := utf8.DecodeRuneInString(p)
			ks, ok := KeysymForRune(r)
			if !ok {
				return nil, fmt.Errorf("x11: invalid key %q", p)
			}
			out = append(out, ks)
			continue
		}
		lower := strings.ToLower(p)
		if ks, ok := comboNames[lower]; ok {
			out = append(out, ks)
			continue
		}
		var n int
		if _, err := fmt.Sscanf(lower, "f%d", &n); err == nil && n >= 1 && n <= 24 && fmt.Sprintf("f%d", n) == lower {
			out = append(out, XKF1+uint32(n-1))
			continue
		}
		if ks, ok := domNamedKeys[p]; ok {
			out = append(out, ks)
			continue
		}
		return nil, fmt.Errorf("x11: unknown key %q in combo %q", p, combo)
	}
	return out, nil
}
