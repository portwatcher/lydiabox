// Package acp is a small Agent Client Protocol client
// (https://agentclientprotocol.com, protocol version 1) and the bridge that
// runs it in the guest. The bridge starts the ACP agent of a harness, drives
// one session, turns its updates into harness events, and talks to the host
// with JSON lines over its terminal (docs/harness-formats.md, "ACP").
//
// The client decodes the agent messages leniently (raw JSON per variant),
// so the five agents, which follow different schema versions, all work.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("%s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (%d)", e.Message, e.Code)
}

// JSON-RPC error codes.
const (
	CodeMethodNotFound = -32601
	CodeInternal       = -32603
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Handler answers a request of the agent (result, error) or takes a
// notification (id is nil; the result is dropped).
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Conn is a JSON-RPC 2.0 connection over newline-delimited JSON.
type Conn struct {
	w       io.Writer
	handler Handler
	writeMu sync.Mutex

	mu      sync.Mutex
	next    int64
	pending map[string]chan message
	done    chan struct{}
	err     error
}

// MaxLine bounds one JSON-RPC message (a session/load replay can be large).
const MaxLine = 64 << 20

// NewConn starts reading r. Requests and notifications of the peer go to h:
// notifications in order on one goroutine, requests each on its own.
func NewConn(r io.Reader, w io.Writer, h Handler) *Conn {
	c := &Conn{w: w, handler: h, pending: map[string]chan message{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

// Done is closed when the peer closed its output.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err is the read error after Done.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), MaxLine)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for sc.Scan() {
		var m message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue // agents may log to stdout; such lines are not messages
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			go c.answer(ctx, m)
		case m.Method != "":
			if c.handler != nil {
				_, _ = c.handler(ctx, m.Method, m.Params)
			}
		case len(m.ID) > 0:
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			delete(c.pending, string(m.ID))
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	c.err = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	close(c.done)
}

func (c *Conn) answer(ctx context.Context, m message) {
	reply := message{JSONRPC: "2.0", ID: m.ID}
	if c.handler == nil {
		reply.Error = &RPCError{Code: CodeMethodNotFound, Message: "method not found"}
	} else if res, err := c.handler(ctx, m.Method, m.Params); err != nil {
		var re *RPCError
		if !errors.As(err, &re) {
			re = &RPCError{Code: CodeInternal, Message: err.Error()}
		}
		reply.Error = re
	} else {
		b, merr := json.Marshal(res)
		if merr != nil {
			reply.Error = &RPCError{Code: CodeInternal, Message: merr.Error()}
		} else {
			reply.Result = b
		}
	}
	_ = c.send(reply)
}

func (c *Conn) send(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// Call sends a request and decodes its result into out (nil to drop it).
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan message, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("acp: connection closed: %w", err)
	}
	c.next++
	id := json.RawMessage(strconv.FormatInt(c.next, 10))
	c.pending[string(id)] = ch
	c.mu.Unlock()
	if err := c.send(message{ID: id, Method: method, Params: p}); err != nil {
		c.forget(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return fmt.Errorf("acp: %s: the agent closed the connection", method)
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	}
}

func (c *Conn) forget(id json.RawMessage) {
	c.mu.Lock()
	delete(c.pending, string(id))
	c.mu.Unlock()
}

// Notify sends a notification.
func (c *Conn) Notify(method string, params any) error {
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(message{Method: method, Params: p})
}
