package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// conn is a minimal Chrome DevTools Protocol client on one browser-level
// WebSocket. Page commands go through flattened target sessions
// (Target.attachToTarget with flatten), so one socket reaches every tab.
// Events are not used and are dropped.
type conn struct {
	ws *websocket.Conn

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan cdpResponse
	err     error
	done    chan struct{}
}

type cdpRequest struct {
	ID        int64  `json:"id"`
	Method    string `json:"method"`
	Params    any    `json:"params,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type cdpResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *cdpError       `json:"error"`
}

// cdpError is a protocol error from Chrome.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *cdpError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Data)
	}
	return e.Message
}

// errClosed is returned by calls on a connection that has failed.
var errClosed = errors.New("chrome devtools connection closed")

// maxMessage bounds one CDP message; full-page screenshots are large.
const maxMessage = 64 << 20

// dial connects to the browser WebSocket URL. Chrome rejects WebSocket
// handshakes with an Origin header unless --remote-allow-origins is set;
// this client sends none.
func dial(ctx context.Context, url string) (*conn, error) {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to chrome devtools: %w", err)
	}
	ws.SetReadLimit(maxMessage)
	c := &conn{ws: ws, pending: map[int64]chan cdpResponse{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *conn) readLoop() {
	var err error
	for {
		var data []byte
		_, data, err = c.ws.Read(context.Background())
		if err != nil {
			break
		}
		var resp cdpResponse
		if json.Unmarshal(data, &resp) != nil || resp.ID == 0 {
			continue // an event or an unparseable message
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	c.mu.Lock()
	c.err = errors.Join(errClosed, err)
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

// alive reports whether the read loop still runs.
func (c *conn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// call sends method to the browser (sessionID "") or to a page session
// and decodes the result into out (when out is not nil).
func (c *conn) call(ctx context.Context, sessionID, method string, params, out any) error {
	ch := make(chan cdpResponse, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	data, err := json.Marshal(cdpRequest{ID: id, Method: method, Params: params, SessionID: sessionID})
	if err != nil {
		c.forget(id)
		return err
	}
	c.writeMu.Lock()
	err = c.ws.Write(ctx, websocket.MessageText, data)
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return fmt.Errorf("%s: %w", method, err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			return err
		}
		if resp.Error != nil {
			return fmt.Errorf("%s: %w", method, resp.Error)
		}
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("%s: decode result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *conn) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *conn) close() {
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
}

// isSessionGone reports whether err means that a page session no longer
// exists (the tab closed or the target detached).
func isSessionGone(err error) bool {
	var ce *cdpError
	if !errors.As(err, &ce) {
		return false
	}
	m := strings.ToLower(ce.Message)
	return strings.Contains(m, "session") || strings.Contains(m, "no target")
}
