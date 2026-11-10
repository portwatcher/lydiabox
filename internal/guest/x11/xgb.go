package x11

import (
	"errors"
	"fmt"
	"image"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Backend is the X server seen by the controller. The xgb implementation
// talks to Xvfb; tests use a fake.
type Backend interface {
	// Size returns the root window size in pixels.
	Size() (int, int)
	// Capture grabs the root window.
	Capture() (*image.RGBA, error)
	// Move moves the pointer to absolute coordinates.
	Move(x, y int) error
	// Button presses (down) or releases an X pointer button (1 left,
	// 2 middle, 3 right, 4/5 vertical wheel, 6/7 horizontal wheel).
	Button(button uint8, down bool) error
	// Key presses or releases the key that produces keysym.
	Key(keysym uint32, down bool) error
	Close() error
}

// XGB is a Backend on a pure-Go X11 connection with the XTEST extension.
type XGB struct {
	conn *xgb.Conn
	root xproto.Window
	w, h int

	mu        sync.Mutex
	minKC     xproto.Keycode
	perKC     int
	keymap    []xproto.Keysym
	scratch   []xproto.Keycode
	nextSpare int
	remapped  map[uint32]xproto.Keycode
	shifted   map[uint32]bool
}

// DialXGB connects to display (for example ":0") and initializes XTEST.
func DialXGB(display string) (*XGB, error) {
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("x11: connect %s: %w", display, err)
	}
	if err := xtest.Init(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("x11: XTEST extension: %w", err)
	}
	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	x := &XGB{
		conn: conn, root: screen.Root, w: int(screen.WidthInPixels), h: int(screen.HeightInPixels),
		remapped: map[uint32]xproto.Keycode{}, shifted: map[uint32]bool{},
	}
	if err := x.loadKeymap(); err != nil {
		conn.Close()
		return nil, err
	}
	return x, nil
}

func (x *XGB) loadKeymap() error {
	setup := xproto.Setup(x.conn)
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	rep, err := xproto.GetKeyboardMapping(x.conn, setup.MinKeycode, count).Reply()
	if err != nil {
		return fmt.Errorf("x11: keyboard mapping: %w", err)
	}
	x.minKC = setup.MinKeycode
	x.perKC = int(rep.KeysymsPerKeycode)
	x.keymap = rep.Keysyms
	x.scratch = x.scratch[:0]
	for i := 0; i*x.perKC < len(x.keymap); i++ {
		empty := true
		for j := 0; j < x.perKC; j++ {
			if x.keymap[i*x.perKC+j] != 0 {
				empty = false
				break
			}
		}
		if empty {
			x.scratch = append(x.scratch, x.minKC+xproto.Keycode(i))
		}
	}
	return nil
}

// Size implements Backend.
func (x *XGB) Size() (int, int) { return x.w, x.h }

// Capture implements Backend. The image is read in horizontal strips to stay
// well under the X11 maximum reply size.
func (x *XGB) Capture() (*image.RGBA, error) {
	img := image.NewRGBA(image.Rect(0, 0, x.w, x.h))
	const strip = 128
	for y := 0; y < x.h; y += strip {
		h := min(strip, x.h-y)
		rep, err := xproto.GetImage(x.conn, xproto.ImageFormatZPixmap, xproto.Drawable(x.root),
			0, int16(y), uint16(x.w), uint16(h), 0xffffffff).Reply()
		if err != nil {
			return nil, fmt.Errorf("x11: GetImage: %w", err)
		}
		if err := convertBGRX(img, rep.Data, y, x.w, h); err != nil {
			return nil, err
		}
	}
	return img, nil
}

// convertBGRX copies 32-bit little-endian ZPixmap rows (B, G, R, X) into img.
func convertBGRX(img *image.RGBA, data []byte, y0, w, h int) error {
	if len(data) < w*h*4 {
		return fmt.Errorf("x11: GetImage returned %d bytes for %dx%d (unsupported depth)", len(data), w, h)
	}
	for row := 0; row < h; row++ {
		src := data[row*w*4 : (row+1)*w*4]
		dst := img.Pix[(y0+row)*img.Stride : (y0+row)*img.Stride+w*4]
		for i := 0; i < len(src); i += 4 {
			dst[i], dst[i+1], dst[i+2], dst[i+3] = src[i+2], src[i+1], src[i], 0xff
		}
	}
	return nil
}

func (x *XGB) fake(typ, detail byte, px, py int16) error {
	return xtest.FakeInputChecked(x.conn, typ, detail, 0, x.root, px, py, 0).Check()
}

// Move implements Backend.
func (x *XGB) Move(px, py int) error {
	return x.fake(xproto.MotionNotify, 0, int16(px), int16(py))
}

// Button implements Backend.
func (x *XGB) Button(button uint8, down bool) error {
	typ := byte(xproto.ButtonRelease)
	if down {
		typ = xproto.ButtonPress
	}
	return x.fake(typ, button, 0, 0)
}

// Key implements Backend. A keysym on the shifted level of its key is typed
// with Shift; a keysym absent from the keymap is bound to a spare keycode.
func (x *XGB) Key(keysym uint32, down bool) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	kc, shift, err := x.keycodeFor(keysym)
	if err != nil {
		return err
	}
	shiftKC := xproto.Keycode(0)
	if shift {
		if s, _, err := x.keycodeFor(XKShiftL); err == nil {
			shiftKC = s
		}
	}
	if down {
		if shiftKC != 0 {
			if err := x.fake(xproto.KeyPress, byte(shiftKC), 0, 0); err != nil {
				return err
			}
			x.shifted[keysym] = true
		}
		return x.fake(xproto.KeyPress, byte(kc), 0, 0)
	}
	err = x.fake(xproto.KeyRelease, byte(kc), 0, 0)
	if x.shifted[keysym] && shiftKC != 0 {
		delete(x.shifted, keysym)
		err = errors.Join(err, x.fake(xproto.KeyRelease, byte(shiftKC), 0, 0))
	}
	return err
}

func (x *XGB) keycodeFor(keysym uint32) (xproto.Keycode, bool, error) {
	if kc, ok := x.remapped[keysym]; ok {
		return kc, false, nil
	}
	for i := 0; i*x.perKC < len(x.keymap); i++ {
		row := x.keymap[i*x.perKC : (i+1)*x.perKC]
		if len(row) > 0 && uint32(row[0]) == keysym {
			return x.minKC + xproto.Keycode(i), false, nil
		}
	}
	for i := 0; i*x.perKC < len(x.keymap); i++ {
		row := x.keymap[i*x.perKC : (i+1)*x.perKC]
		if len(row) > 1 && uint32(row[1]) == keysym {
			return x.minKC + xproto.Keycode(i), true, nil
		}
	}
	return x.remap(keysym)
}

// remap binds keysym to a spare keycode, reusing spares round-robin.
func (x *XGB) remap(keysym uint32) (xproto.Keycode, bool, error) {
	if len(x.scratch) == 0 {
		return 0, false, fmt.Errorf("x11: keysym 0x%x is not mapped and no spare keycode exists", keysym)
	}
	kc := x.scratch[x.nextSpare%len(x.scratch)]
	x.nextSpare++
	for ks, c := range x.remapped {
		if c == kc {
			delete(x.remapped, ks)
		}
	}
	syms := make([]xproto.Keysym, x.perKC)
	for i := range syms {
		syms[i] = xproto.Keysym(keysym)
	}
	if err := xproto.ChangeKeyboardMappingChecked(x.conn, 1, kc, byte(x.perKC), syms).Check(); err != nil {
		return 0, false, fmt.Errorf("x11: remap keycode %d: %w", kc, err)
	}
	x.remapped[keysym] = kc
	return kc, false, nil
}

// Close implements Backend.
func (x *XGB) Close() error {
	x.conn.Close()
	return nil
}
