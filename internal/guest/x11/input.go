package x11

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// InputMessage is one message of the WebRTC "input" data channel
// (docs/api.md section 9).
type InputMessage struct {
	T      string  `json:"t"`
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Button string  `json:"button"`
	DX     float64 `json:"dx"`
	DY     float64 `json:"dy"`
	Code   string  `json:"code"`
	Key    string  `json:"key"`
	Text   string  `json:"text"`
}

// wheelUnit is the DOM wheel delta of one notch (pixels).
const wheelUnit = 100.0

// HandleInputJSON decodes and injects one data channel message.
func (c *Controller) HandleInputJSON(data []byte) error {
	var m InputMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("x11: invalid input message: %w", err)
	}
	return c.HandleInput(m)
}

// HandleInput injects browser input. Messages are ignored (nil error) in
// OBSERVER mode, so a stale browser cannot fight the agent.
func (c *Controller) HandleInput(m InputMessage) error {
	if c.Mode().Mode != rpc.ModeTakeover {
		return nil
	}
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	be, err := c.backend()
	if err != nil {
		return err
	}
	return c.drop(be, c.inject(be, m))
}

func (c *Controller) inject(be Backend, m InputMessage) error {
	switch m.T {
	case "move":
		x, y := c.clamp(m.X, m.Y)
		return be.Move(x, y)
	case "down", "up":
		btn, err := ButtonNumber(m.Button)
		if err != nil {
			return err
		}
		down := m.T == "down"
		c.trackButton(btn, down)
		return be.Button(btn, down)
	case "wheel":
		return wheel(be, notches(m.DX), notches(m.DY))
	case "keydown", "keyup":
		ks, err := KeysymForDOM(m.Code, m.Key)
		if err != nil {
			return err
		}
		down := m.T == "keydown"
		c.trackKey(ks, down)
		return be.Key(ks, down)
	case "text":
		for _, r := range m.Text {
			ks, ok := KeysymForRune(r)
			if !ok {
				continue
			}
			if err := KeyTap(be, []uint32{ks}); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("x11: unknown input message type %q", m.T)
}

func notches(delta float64) int {
	if delta == 0 {
		return 0
	}
	n := int(math.Round(delta / wheelUnit))
	if n == 0 {
		if delta > 0 {
			return 1
		}
		return -1
	}
	return n
}

func (c *Controller) trackKey(ks uint32, down bool) {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if down {
		c.userKeys[ks] = true
	} else {
		delete(c.userKeys, ks)
	}
}

func (c *Controller) trackButton(b uint8, down bool) {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if down {
		c.userBtns[b] = true
	} else {
		delete(c.userBtns, b)
	}
}

// EncodePNG encodes img as PNG.
func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeJPEG encodes img as JPEG with quality 1..100 (default 80).
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 80
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
