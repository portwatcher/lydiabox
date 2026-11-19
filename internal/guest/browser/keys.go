package browser

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// keyEvent is one key of Input.dispatchKeyEvent.
type keyEvent struct {
	key, code string
	vk        int
	text      string
	mods      int
	// commands are Chrome editing commands that the shortcut runs; they
	// make ctrl+a and friends work the same on every platform.
	commands []string
}

// CDP modifier bits.
const (
	modAlt   = 1
	modCtrl  = 2
	modMeta  = 4
	modShift = 8
)

var namedKeys = map[string]keyEvent{
	"enter":      {key: "Enter", code: "Enter", vk: 13, text: "\r"},
	"tab":        {key: "Tab", code: "Tab", vk: 9},
	"escape":     {key: "Escape", code: "Escape", vk: 27},
	"backspace":  {key: "Backspace", code: "Backspace", vk: 8},
	"delete":     {key: "Delete", code: "Delete", vk: 46},
	"space":      {key: " ", code: "Space", vk: 32, text: " "},
	"arrowup":    {key: "ArrowUp", code: "ArrowUp", vk: 38},
	"arrowdown":  {key: "ArrowDown", code: "ArrowDown", vk: 40},
	"arrowleft":  {key: "ArrowLeft", code: "ArrowLeft", vk: 37},
	"arrowright": {key: "ArrowRight", code: "ArrowRight", vk: 39},
	"home":       {key: "Home", code: "Home", vk: 36},
	"end":        {key: "End", code: "End", vk: 35},
	"pageup":     {key: "PageUp", code: "PageUp", vk: 33},
	"pagedown":   {key: "PageDown", code: "PageDown", vk: 34},
	"insert":     {key: "Insert", code: "Insert", vk: 45},
}

var keyAliases = map[string]string{
	"return": "enter", "esc": "escape", "del": "delete", "up": "arrowup", "down": "arrowdown",
	"left": "arrowleft", "right": "arrowright", "pgup": "pageup", "pgdn": "pagedown",
}

var modifierNames = map[string]int{
	"ctrl": modCtrl, "control": modCtrl, "alt": modAlt, "option": modAlt, "shift": modShift,
	"meta": modMeta, "cmd": modMeta, "command": modMeta, "super": modMeta,
}

// parseCombo parses "Enter", "ctrl+a", "ctrl+shift+Tab", or one character.
func parseCombo(combo string) (keyEvent, error) {
	combo = strings.TrimSpace(combo)
	if combo == "" {
		return keyEvent{}, fmt.Errorf("key is required (for example Enter, Tab, ctrl+a)")
	}
	parts := strings.Split(combo, "+")
	if strings.HasSuffix(combo, "++") || combo == "+" {
		parts = append(strings.Split(strings.TrimSuffix(combo, "++"), "+"), "+")
		if combo == "+" {
			parts = []string{"+"}
		}
	}
	mods := 0
	for _, m := range parts[:len(parts)-1] {
		bit, ok := modifierNames[strings.ToLower(strings.TrimSpace(m))]
		if !ok {
			return keyEvent{}, fmt.Errorf("unknown modifier %q in %q (use ctrl, alt, shift, meta)", m, combo)
		}
		mods |= bit
	}
	k, err := keyFor(strings.TrimSpace(parts[len(parts)-1]), mods)
	if err != nil {
		return keyEvent{}, err
	}
	k.mods = mods
	if mods&(modCtrl|modAlt|modMeta) != 0 {
		k.text = "" // a shortcut, not text input
	}
	if mods&(modCtrl|modMeta) != 0 && mods&modAlt == 0 {
		shift := mods&modShift != 0
		switch strings.ToLower(k.key) {
		case "a":
			k.commands = []string{"selectAll"}
		case "c":
			k.commands = []string{"copy"}
		case "x":
			k.commands = []string{"cut"}
		case "v":
			k.commands = []string{"paste"}
		case "z":
			k.commands = []string{"undo"}
			if shift {
				k.commands = []string{"redo"}
			}
		}
	}
	return k, nil
}

func keyFor(name string, mods int) (keyEvent, error) {
	lower := strings.ToLower(name)
	if a, ok := keyAliases[lower]; ok {
		lower = a
	}
	if k, ok := namedKeys[lower]; ok {
		return k, nil
	}
	if len(lower) >= 2 && lower[0] == 'f' {
		if n, err := strconv.Atoi(lower[1:]); err == nil && n >= 1 && n <= 12 {
			return keyEvent{key: "F" + lower[1:], code: "F" + lower[1:], vk: 111 + n}, nil
		}
	}
	if utf8.RuneCountInString(name) != 1 {
		return keyEvent{}, fmt.Errorf("unknown key %q (use a name such as Enter, Tab, Escape, ArrowDown, F5, or one character)", name)
	}
	r, _ := utf8.DecodeRuneInString(name)
	k := keyEvent{key: name, text: name}
	switch {
	case r < 128 && unicode.IsLetter(r):
		up := unicode.ToUpper(r)
		k.code, k.vk = "Key"+string(up), int(up)
		if mods&modShift != 0 {
			k.key, k.text = string(up), string(up)
		} else {
			k.key, k.text = string(unicode.ToLower(r)), string(unicode.ToLower(r))
		}
	case r >= '0' && r <= '9':
		k.code, k.vk = "Digit"+name, int(r)
	}
	return k, nil
}
