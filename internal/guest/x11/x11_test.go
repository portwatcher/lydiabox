package x11

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

func newTestController(wait time.Duration) (*Controller, *Fake) {
	f := NewFake(1920, 1080)
	c := NewController(Options{Dial: func() (Backend, error) { return f, nil }, TakeoverWait: wait})
	return c, f
}

func TestKeysymMapping(t *testing.T) {
	cases := map[rune]uint32{'a': 0x61, 'Z': 0x5a, ' ': 0x20, 'é': 0xe9, '€': 0x010020ac, '中': 0x01004e2d, '\n': XKReturn}
	for r, want := range cases {
		if got, ok := KeysymForRune(r); !ok || got != want {
			t.Errorf("KeysymForRune(%q) = %x, %v; want %x", r, got, ok, want)
		}
	}
	if _, ok := KeysymForRune(0x07); ok {
		t.Error("control character mapped")
	}
	dom := []struct {
		code, key string
		want      uint32
	}{
		{"KeyA", "A", 'a'}, {"Digit1", "!", '1'}, {"Enter", "Enter", XKReturn}, {"ShiftLeft", "Shift", XKShiftL},
		{"ControlRight", "Control", XKControlR}, {"F5", "F5", XKF1 + 4}, {"ArrowUp", "ArrowUp", XKUp},
		{"Slash", "/", '/'}, {"", "ü", 0xfc}, {"Unidentified", "Escape", XKEscape},
	}
	for _, c := range dom {
		if got, err := KeysymForDOM(c.code, c.key); err != nil || got != c.want {
			t.Errorf("KeysymForDOM(%q, %q) = %x, %v; want %x", c.code, c.key, got, err, c.want)
		}
	}
	if _, err := KeysymForDOM("Fn", "Dead"); err == nil {
		t.Error("unmappable key accepted")
	}
	got, err := ParseCombo("ctrl+Shift+t")
	if err != nil || !reflect.DeepEqual(got, []uint32{XKControlL, XKShiftL, 't'}) {
		t.Fatalf("ParseCombo = %x, %v", got, err)
	}
	if got, err := ParseCombo("F12"); err != nil || got[0] != XKF1+11 {
		t.Fatalf("F12 = %x, %v", got, err)
	}
	if _, err := ParseCombo("ctrl+bogus"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestAgentActionsInjectAndEmitEvents(t *testing.T) {
	c, f := newTestController(time.Second)
	events, cancel := c.Events().Subscribe()
	defer cancel()
	ctx := context.Background()
	if err := c.Click(ctx, "computer_mouse_click", 812, 440, "left", true); err != nil {
		t.Fatal(err)
	}
	want := []string{"move 812 440", "button 1 down", "button 1 up", "button 1 down", "button 1 up"}
	if got := f.Events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v", got)
	}
	var ev AgentAction
	if err := json.Unmarshal(<-events, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Type != "agent_action" || ev.Action != "click" || ev.X != 812 || !ev.Double || ev.Tool != "computer_mouse_click" {
		t.Fatalf("event %+v", ev)
	}

	f.Reset()
	if err := c.KeyCombo(ctx, "computer_keyboard", "ctrl+l"); err != nil {
		t.Fatal(err)
	}
	want = []string{"key 0xffe3 down", "key 0x6c down", "key 0x6c up", "key 0xffe3 up"}
	if got := f.Events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events %v", got)
	}
	f.Reset()
	if err := c.TypeText(ctx, "computer_keyboard", "hé"); err != nil {
		t.Fatal(err)
	}
	if got := f.Events(); len(got) != 4 || got[2] != "key 0xe9 down" {
		t.Fatalf("events %v", got)
	}
	f.Reset()
	if err := c.Scroll(ctx, "computer_scroll", 5000, -3, 0, 3); err != nil {
		t.Fatal(err)
	}
	if got := f.Events(); got[0] != "move 1919 0" || len(got) != 7 || got[1] != "button 5 down" {
		t.Fatalf("scroll events %v", got)
	}
	img, err := c.Screenshot(ctx, "computer_screenshot")
	if err != nil || img.Bounds() != image.Rect(0, 0, 1920, 1080) {
		t.Fatalf("screenshot %v %v", img.Bounds(), err)
	}
	if b, err := EncodePNG(img); err != nil || len(b) == 0 {
		t.Fatal(err)
	}
}

func TestTakeoverBlocksAgentAndInjectsUserInput(t *testing.T) {
	c, f := newTestController(50 * time.Millisecond)
	ctx := context.Background()

	// OBSERVER: browser input is ignored.
	if err := c.HandleInputJSON([]byte(`{"t":"move","x":10,"y":10}`)); err != nil {
		t.Fatal(err)
	}
	if len(f.Events()) != 0 {
		t.Fatalf("observer input injected: %v", f.Events())
	}

	events, cancel := c.Events().Subscribe()
	defer cancel()
	st, err := c.SetMode(rpc.ModeTakeover, "user")
	if err != nil || st.Mode != rpc.ModeTakeover || st.ChangedBy != "user" {
		t.Fatalf("SetMode %+v %v", st, err)
	}
	var mode ModeEvent
	if err := json.Unmarshal(<-events, &mode); err != nil || mode.Type != "mode" || mode.Mode != rpc.ModeTakeover {
		t.Fatalf("mode event %+v %v", mode, err)
	}

	err = c.Click(ctx, "computer_mouse_click", 1, 1, "left", false)
	if !errors.Is(err, ErrTakeover) || !strings.Contains(err.Error(), "manual user takeover") {
		t.Fatalf("agent click during takeover: %v", err)
	}
	for _, msg := range []string{
		`{"t":"move","x":640,"y":360}`, `{"t":"down","button":"right"}`, `{"t":"keydown","code":"ShiftLeft","key":"Shift"}`,
		`{"t":"keydown","code":"KeyA","key":"A"}`, `{"t":"keyup","code":"KeyA","key":"A"}`, `{"t":"wheel","dx":0,"dy":-120}`,
		`{"t":"text","text":"ö"}`,
	} {
		if err := c.HandleInputJSON([]byte(msg)); err != nil {
			t.Fatalf("%s: %v", msg, err)
		}
	}
	want := []string{"move 640 360", "button 3 down", "key 0xffe1 down", "key 0x61 down", "key 0x61 up",
		"button 4 down", "button 4 up", "key 0xf6 down", "key 0xf6 up"}
	if got := f.Events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("user events %v", got)
	}

	// An agent action waiting for handback proceeds once the user returns
	// control, and held keys and buttons are released.
	c.opts.TakeoverWait = 5 * time.Second
	done := make(chan error, 1)
	go func() { done <- c.MoveTo(ctx, "computer_mouse_move", 3, 4) }()
	time.Sleep(30 * time.Millisecond)
	f.Reset()
	if _, err := c.SetMode(rpc.ModeObserver, "user"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.Events(), ",")
	for _, w := range []string{"key 0xffe1 up", "button 3 up", "move 3 4"} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %q in %s", w, got)
		}
	}
	if _, err := c.SetMode("BOGUS", "user"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
