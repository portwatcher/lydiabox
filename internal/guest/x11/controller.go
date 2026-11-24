// Package x11 drives the guest X11 desktop: screen capture and synthetic
// input through pure-Go xgb/XTEST, the OBSERVER/TAKEOVER control mode, and
// the agent_action event stream shown as the action HUD in the web UI.
package x11

import (
	"context"
	"errors"
	"image"
	"sync"
	"time"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// TakeoverMessage is returned to an agent whose input action could not run
// because the user holds the desktop.
const TakeoverMessage = "Desktop is currently under manual user takeover; wait or re-screenshot after user hands control back"

// DefaultTakeoverWait is how long an agent action waits for the user to hand
// control back.
const DefaultTakeoverWait = 30 * time.Second

// ErrTakeover is returned by agent actions that timed out in TAKEOVER mode.
var ErrTakeover = &rpc.Error{Code: rpc.CodeTakeover, Message: TakeoverMessage}

// Options configures a Controller.
type Options struct {
	// Dial opens the X connection. It is called lazily and again after a
	// failure, so the daemon can start before Xvfb.
	Dial func() (Backend, error)
	// TakeoverWait overrides DefaultTakeoverWait.
	TakeoverWait time.Duration
	// Now overrides time.Now (tests).
	Now func() time.Time
}

// Controller serializes desktop input and owns the control mode.
type Controller struct {
	opts Options
	hub  *Hub

	beMu sync.Mutex
	be   Backend

	// inputMu serializes complete input sequences (a double click, a drag,
	// typed text) so agent and user input never interleave.
	inputMu sync.Mutex

	modeMu   sync.Mutex
	mode     rpc.ModeState
	handback chan struct{}
	userKeys map[uint32]bool
	userBtns map[uint8]bool
}

// NewController returns a controller in OBSERVER mode.
func NewController(opts Options) *Controller {
	if opts.TakeoverWait <= 0 {
		opts.TakeoverWait = DefaultTakeoverWait
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	c := &Controller{
		opts: opts, hub: NewHub(), handback: make(chan struct{}),
		userKeys: map[uint32]bool{}, userBtns: map[uint8]bool{},
	}
	close(c.handback)
	c.mode = rpc.ModeState{Mode: rpc.ModeObserver, ChangedAt: opts.Now().UTC(), ChangedBy: "system"}
	return c
}

// Events returns the event hub (agent_action and mode events).
func (c *Controller) Events() *Hub { return c.hub }

func (c *Controller) backend() (Backend, error) {
	c.beMu.Lock()
	defer c.beMu.Unlock()
	if c.be != nil {
		return c.be, nil
	}
	if c.opts.Dial == nil {
		return nil, rpc.Errorf(rpc.CodeUnavailable, "no X display configured")
	}
	be, err := c.opts.Dial()
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeUnavailable, "X display unavailable: %v", err)
	}
	c.be = be
	return be, nil
}

// drop forgets a failed connection so the next call reconnects.
func (c *Controller) drop(be Backend, err error) error {
	if err == nil {
		return nil
	}
	c.beMu.Lock()
	if c.be == be {
		c.be = nil
		be.Close()
	}
	c.beMu.Unlock()
	return err
}

// ScreenSize returns the desktop size, or 1920x1080 when X is not reachable.
func (c *Controller) ScreenSize() (int, int) {
	be, err := c.backend()
	if err != nil {
		return 1920, 1080
	}
	return be.Size()
}

// Mode returns the current mode.
func (c *Controller) Mode() rpc.ModeState {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	return c.mode
}

// SetMode switches between OBSERVER and TAKEOVER. Returning to OBSERVER
// releases keys and buttons the user still held and wakes waiting agents.
func (c *Controller) SetMode(mode, by string) (rpc.ModeState, error) {
	if mode != rpc.ModeObserver && mode != rpc.ModeTakeover {
		return rpc.ModeState{}, rpc.Errorf(rpc.CodeBadRequest, "mode must be %s or %s", rpc.ModeObserver, rpc.ModeTakeover)
	}
	switch by {
	case "user", "agent", "system":
	case "":
		by = "user"
	default:
		return rpc.ModeState{}, rpc.Errorf(rpc.CodeBadRequest, "changedBy must be user, agent, or system")
	}
	c.modeMu.Lock()
	if c.mode.Mode == mode {
		st := c.mode
		c.modeMu.Unlock()
		return st, nil
	}
	c.mode = rpc.ModeState{Mode: mode, ChangedAt: c.opts.Now().UTC(), ChangedBy: by}
	var keys []uint32
	var btns []uint8
	if mode == rpc.ModeTakeover {
		c.handback = make(chan struct{})
	} else {
		close(c.handback)
		for k := range c.userKeys {
			keys = append(keys, k)
		}
		for b := range c.userBtns {
			btns = append(btns, b)
		}
		c.userKeys = map[uint32]bool{}
		c.userBtns = map[uint8]bool{}
	}
	st := c.mode
	c.modeMu.Unlock()
	if len(keys)+len(btns) > 0 {
		c.releaseHeld(keys, btns)
	}
	c.hub.Publish(ModeEvent{Type: "mode", Mode: st.Mode, ChangedBy: st.ChangedBy, ChangedAt: st.ChangedAt})
	return st, nil
}

func (c *Controller) releaseHeld(keys []uint32, btns []uint8) {
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	be, err := c.backend()
	if err != nil {
		return
	}
	for _, k := range keys {
		_ = be.Key(k, false)
	}
	for _, b := range btns {
		_ = be.Button(b, false)
	}
}

// awaitControl blocks while the user holds the desktop, until deadline.
func (c *Controller) awaitControl(ctx context.Context, deadline time.Time) error {
	c.modeMu.Lock()
	ch := c.handback
	c.modeMu.Unlock()
	select {
	case <-ch:
		return nil
	default:
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-t.C:
		return ErrTakeover
	case <-ctx.Done():
		return ctx.Err()
	}
}

// agentInput runs fn with the input lock held once the agent has control
// (waiting up to TakeoverWait for a handback), then publishes ev.
func (c *Controller) agentInput(ctx context.Context, ev AgentAction, fn func(Backend) error) error {
	return c.Act(ctx, ev, func() error {
		be, err := c.backend()
		if err != nil {
			return err
		}
		return c.drop(be, fn(be))
	})
}

// Act runs an agent action that does not use the X connection (a browser
// action through the Chrome DevTools Protocol) under the same rules as
// X11 input: it waits up to TakeoverWait while the user holds the desktop,
// holds the input lock so it never interleaves with other agent input, and
// publishes ev when fn succeeds. A zero ev.Action publishes nothing.
func (c *Controller) Act(ctx context.Context, ev AgentAction, fn func() error) error {
	deadline := time.Now().Add(c.opts.TakeoverWait)
	for {
		if err := c.awaitControl(ctx, deadline); err != nil {
			return err
		}
		c.inputMu.Lock()
		// The user may have taken over while this action waited for the lock.
		if c.Mode().Mode != rpc.ModeTakeover {
			break
		}
		c.inputMu.Unlock()
	}
	defer c.inputMu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	if ev.Action != "" {
		c.Observe(ev)
	}
	return nil
}

// Observe publishes an agent action that injects no input (a page
// snapshot or a browser screenshot); it is allowed in TAKEOVER mode.
func (c *Controller) Observe(ev AgentAction) {
	ev.Type = "agent_action"
	ev.Timestamp = c.opts.Now().UTC()
	c.hub.Publish(ev)
}

// Screenshot captures the screen. It is allowed in TAKEOVER mode because it
// injects no input.
func (c *Controller) Screenshot(_ context.Context, tool string) (*image.RGBA, error) {
	be, err := c.backend()
	if err != nil {
		return nil, err
	}
	img, err := be.Capture()
	if err != nil {
		return nil, c.drop(be, err)
	}
	c.hub.Publish(AgentAction{Type: "agent_action", Action: "screenshot", Tool: tool, Timestamp: c.opts.Now().UTC()})
	return img, nil
}

// ButtonNumber maps left/middle/right to X button numbers.
func ButtonNumber(name string) (uint8, error) {
	switch name {
	case "", "left":
		return 1, nil
	case "middle":
		return 2, nil
	case "right":
		return 3, nil
	}
	return 0, rpc.Errorf(rpc.CodeBadRequest, "button must be left, middle, or right")
}

func (c *Controller) clamp(x, y int) (int, int) {
	w, h := c.ScreenSize()
	return max(0, min(x, w-1)), max(0, min(y, h-1))
}

// Click moves to (x, y) and clicks button, twice when double is set.
func (c *Controller) Click(ctx context.Context, tool string, x, y int, button string, double bool) error {
	btn, err := ButtonNumber(button)
	if err != nil {
		return err
	}
	x, y = c.clamp(x, y)
	if button == "" {
		button = "left"
	}
	ev := AgentAction{Action: "click", X: x, Y: y, Button: button, Double: double, Tool: tool}
	return c.agentInput(ctx, ev, func(be Backend) error {
		if err := be.Move(x, y); err != nil {
			return err
		}
		clicks := 1
		if double {
			clicks = 2
		}
		for i := 0; i < clicks; i++ {
			if err := be.Button(btn, true); err != nil {
				return err
			}
			if err := be.Button(btn, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// MoveTo moves the pointer.
func (c *Controller) MoveTo(ctx context.Context, tool string, x, y int) error {
	x, y = c.clamp(x, y)
	return c.agentInput(ctx, AgentAction{Action: "move", X: x, Y: y, Tool: tool}, func(be Backend) error {
		return be.Move(x, y)
	})
}

// Drag presses button at (fromX, fromY), moves in steps to (x, y), releases.
func (c *Controller) Drag(ctx context.Context, tool string, fromX, fromY, x, y int, button string) error {
	btn, err := ButtonNumber(button)
	if err != nil {
		return err
	}
	if button == "" {
		button = "left"
	}
	fromX, fromY = c.clamp(fromX, fromY)
	x, y = c.clamp(x, y)
	ev := AgentAction{Action: "drag", X: x, Y: y, FromX: fromX, FromY: fromY, Button: button, Tool: tool}
	return c.agentInput(ctx, ev, func(be Backend) error {
		if err := be.Move(fromX, fromY); err != nil {
			return err
		}
		if err := be.Button(btn, true); err != nil {
			return err
		}
		const steps = 10
		var moveErr error
		for i := 1; i <= steps && moveErr == nil; i++ {
			moveErr = be.Move(fromX+(x-fromX)*i/steps, fromY+(y-fromY)*i/steps)
			time.Sleep(8 * time.Millisecond)
		}
		return errors.Join(moveErr, be.Button(btn, false))
	})
}

// Scroll moves to (x, y) and turns the wheel: dy > 0 scrolls down, dx > 0
// right, one unit per wheel click.
func (c *Controller) Scroll(ctx context.Context, tool string, x, y, dx, dy int) error {
	x, y = c.clamp(x, y)
	ev := AgentAction{Action: "scroll", X: x, Y: y, DX: dx, DY: dy, Tool: tool}
	return c.agentInput(ctx, ev, func(be Backend) error {
		if err := be.Move(x, y); err != nil {
			return err
		}
		return wheel(be, dx, dy)
	})
}

func wheel(be Backend, dx, dy int) error {
	click := func(b uint8, n int) error {
		for i := 0; i < n; i++ {
			if err := be.Button(b, true); err != nil {
				return err
			}
			if err := be.Button(b, false); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	switch {
	case dy > 0:
		err = click(5, dy)
	case dy < 0:
		err = click(4, -dy)
	}
	if err != nil {
		return err
	}
	switch {
	case dx > 0:
		return click(7, dx)
	case dx < 0:
		return click(6, -dx)
	}
	return nil
}

// TypeText types UTF-8 text.
func (c *Controller) TypeText(ctx context.Context, tool, text string) error {
	syms := make([]uint32, 0, len(text))
	for _, r := range text {
		ks, ok := KeysymForRune(r)
		if !ok {
			return rpc.Errorf(rpc.CodeBadRequest, "cannot type character %U", r)
		}
		syms = append(syms, ks)
	}
	return c.agentInput(ctx, AgentAction{Action: "type", Text: text, Tool: tool}, func(be Backend) error {
		for _, ks := range syms {
			if err := be.Key(ks, true); err != nil {
				return err
			}
			if err := be.Key(ks, false); err != nil {
				return err
			}
		}
		return nil
	})
}

// KeyCombo presses a combo such as "ctrl+l" and releases it in reverse.
func (c *Controller) KeyCombo(ctx context.Context, tool, combo string) error {
	syms, err := ParseCombo(combo)
	if err != nil {
		return rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	return c.agentInput(ctx, AgentAction{Action: "key", Keys: combo, Tool: tool}, func(be Backend) error {
		return KeyTap(be, syms)
	})
}

// KeyTap presses keysyms in order and releases them in reverse order.
func KeyTap(be Backend, syms []uint32) error {
	pressed := 0
	var err error
	for _, ks := range syms {
		if err = be.Key(ks, true); err != nil {
			break
		}
		pressed++
	}
	for i := pressed - 1; i >= 0; i-- {
		err = errors.Join(err, be.Key(syms[i], false))
	}
	return err
}

// Launched publishes a launch action for app.
func (c *Controller) Launched(tool, app string) {
	c.hub.Publish(AgentAction{Type: "agent_action", Action: "launch", Text: app, Tool: tool, Timestamp: c.opts.Now().UTC()})
}
