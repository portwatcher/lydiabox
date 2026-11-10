package x11

import (
	"fmt"
	"image"
	"image/color"
	"sync"
)

// Fake is an in-memory Backend that records input. It lets the controller,
// the MCP server, and the WebRTC input path run without an X server.
type Fake struct {
	W, H int

	mu     sync.Mutex
	events []string
	closed bool
}

// NewFake returns a fake screen of the given size.
func NewFake(w, h int) *Fake { return &Fake{W: w, H: h} }

func (f *Fake) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fmt.Sprintf(format, args...))
}

// Events returns the recorded input, for example "move 10 20",
// "button 1 down", "key 0x61 up".
func (f *Fake) Events() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// Reset clears the recorded input.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = nil
}

// Size implements Backend.
func (f *Fake) Size() (int, int) { return f.W, f.H }

// Capture implements Backend with a solid image.
func (f *Fake) Capture() (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0x20, 0x40, 0x80, 0xff
	}
	img.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	f.record("capture")
	return img, nil
}

// Move implements Backend.
func (f *Fake) Move(x, y int) error {
	f.record("move %d %d", x, y)
	return nil
}

// Button implements Backend.
func (f *Fake) Button(b uint8, down bool) error {
	f.record("button %d %s", b, upDown(down))
	return nil
}

// Key implements Backend.
func (f *Fake) Key(ks uint32, down bool) error {
	f.record("key 0x%x %s", ks, upDown(down))
	return nil
}

// Close implements Backend.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func upDown(down bool) string {
	if down {
		return "down"
	}
	return "up"
}
