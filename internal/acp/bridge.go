package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"strings"
	"sync"
	"time"

	"github.com/lydiabox/lydiabox/internal/harness"
)

// MCPServer is one HTTP MCP server passed to the agent in session/new.
type MCPServer struct {
	Name string
	URL  string
}

// Options configure one bridge run.
type Options struct {
	Harness string
	// Cwd is the working directory of the session.
	Cwd string
	// Resume is the native session ID to continue ("" for a new session).
	Resume string
	Model  string
	MCP    []MCPServer
	// Home is the guest /home/agent, for the transcript history of an agent
	// that cannot replay a session (session/resume without session/load).
	Home string
	// History reads the transcript events of a session (adapters.EventReader).
	History harness.EventReader
	// In carries host commands; Out carries bridge messages.
	In  io.Reader
	Out io.Writer
	// Agent is the connection to the started agent.
	Agent io.ReadWriter
	// InitTimeout bounds initialize and session setup (default 2 min).
	InitTimeout time.Duration
}

// Bridge drives one ACP session for the host.
type Bridge struct {
	opts Options
	conn *Conn

	mu       sync.Mutex
	outMu    sync.Mutex
	ev       *events
	status   harness.AgentStatus
	session  string
	prompts  chan string
	queued   int
	working  bool
	waiting  map[string]chan string // permission request ID -> option ID
	replay   bool
	stopTurn context.CancelFunc
}

// NewBridge prepares a bridge; Run starts it.
func NewBridge(opts Options) *Bridge {
	if opts.InitTimeout <= 0 {
		opts.InitTimeout = 2 * time.Minute
	}
	b := &Bridge{opts: opts, prompts: make(chan string, 64), waiting: map[string]chan string{},
		status: harness.AgentStatus{State: harness.AgentStarting}}
	b.ev = newEvents(b.emitEvent)
	return b
}

func (b *Bridge) write(m harness.BridgeMessage) {
	line, err := json.Marshal(m)
	if err != nil {
		return
	}
	b.outMu.Lock()
	defer b.outMu.Unlock()
	_, _ = b.opts.Out.Write(append(line, '\n'))
}

// emitEvent runs under b.mu (events change only there).
func (b *Bridge) emitEvent(e harness.Event) {
	ev := e
	b.write(harness.BridgeMessage{Type: harness.BridgeEvent, Event: &ev})
}

// setStatus changes the status under b.mu and sends it.
func (b *Bridge) setStatusLocked(fn func(s *harness.AgentStatus)) {
	fn(&b.status)
	s := b.status
	s.Queued = b.queued
	b.write(harness.BridgeMessage{Type: harness.BridgeStatus, Status: &s})
}

func (b *Bridge) setStatus(fn func(s *harness.AgentStatus)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.setStatusLocked(fn)
}

// Run starts the session and serves host commands until ctx ends, the
// host input ends, or the agent exits. The error tells why the agent ended.
func (b *Bridge) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.conn = NewConn(b.opts.Agent, b.opts.Agent, b.handle)
	go b.readHost(ctx, cancel)
	if err := b.setup(ctx); err != nil {
		b.setStatus(func(s *harness.AgentStatus) { s.State, s.Error = harness.AgentEnded, err.Error() })
		return err
	}
	go b.runPrompts(ctx)
	select {
	case <-ctx.Done():
		b.closeSession()
		return nil
	case <-b.conn.Done():
		err := fmt.Errorf("the agent ended: %w", b.conn.Err())
		b.setStatus(func(s *harness.AgentStatus) { s.State, s.Error = harness.AgentEnded, err.Error() })
		return err
	}
}

// initResult is the part of the initialize result that the bridge uses.
type initResult struct {
	AgentCapabilities struct {
		LoadSession         bool `json:"loadSession"`
		SessionCapabilities struct {
			Resume *json.RawMessage `json:"resume"`
			Close  *json.RawMessage `json:"close"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AgentInfo *struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
	AuthMethods []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"authMethods"`
}

type sessionResult struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions []configOption `json:"configOptions"`
}

type configOption struct {
	ID           string `json:"id"`
	Category     string `json:"category"`
	CurrentValue any    `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
	} `json:"options"`
}

func (b *Bridge) mcpServers() []map[string]any {
	out := []map[string]any{}
	for _, s := range b.opts.MCP {
		out = append(out, map[string]any{"type": "http", "name": s.Name, "url": s.URL, "headers": []any{}})
	}
	return out
}

func (b *Bridge) setup(ctx context.Context) error {
	ictx, cancel := context.WithTimeout(ctx, b.opts.InitTimeout)
	defer cancel()
	var init initResult
	err := b.conn.Call(ictx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]string{"name": "lydiabox", "title": "LydiaBox", "version": "1"},
	}, &init)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	agent := ""
	if init.AgentInfo != nil {
		agent = strings.TrimSpace(init.AgentInfo.Name + " " + init.AgentInfo.Version)
	}
	var res sessionResult
	sessionID := b.opts.Resume
	err = b.withAuth(ictx, init, func() error {
		switch {
		case sessionID == "":
			return b.conn.Call(ictx, "session/new", map[string]any{"cwd": b.opts.Cwd, "mcpServers": b.mcpServers()}, &res)
		case init.AgentCapabilities.LoadSession:
			b.mu.Lock()
			b.replay = true
			b.mu.Unlock()
			defer func() {
				b.mu.Lock()
				b.replay = false
				b.mu.Unlock()
			}()
			return b.conn.Call(ictx, "session/load", map[string]any{"sessionId": sessionID, "cwd": b.opts.Cwd, "mcpServers": b.mcpServers()}, &res)
		case init.AgentCapabilities.SessionCapabilities.Resume != nil:
			b.history(sessionID)
			return b.conn.Call(ictx, "session/resume", map[string]any{"sessionId": sessionID, "cwd": b.opts.Cwd, "mcpServers": b.mcpServers()}, &res)
		}
		return fmt.Errorf("the agent cannot continue a session (no session/load or session/resume)")
	})
	if err != nil {
		return fmt.Errorf("start the session: %w", err)
	}
	if res.SessionID != "" {
		sessionID = res.SessionID
	}
	if sessionID == "" {
		return errors.New("start the session: the agent gave no session ID")
	}
	b.session = sessionID
	mode := ""
	var modes []harness.AgentMode
	if res.Modes != nil {
		mode = res.Modes.CurrentModeID
		for _, m := range res.Modes.AvailableModes {
			modes = append(modes, harness.AgentMode{ID: m.ID, Name: m.Name})
		}
	}
	if want := b.fullAccess(modes); want != "" && want != mode {
		if err := b.conn.Call(ictx, "session/set_mode", map[string]string{"sessionId": sessionID, "modeId": want}, nil); err == nil {
			mode = want
		}
	}
	b.setModel(ictx, res.ConfigOptions)
	b.setStatus(func(s *harness.AgentStatus) {
		s.State, s.NativeID, s.Agent, s.Mode, s.Modes = harness.AgentIdle, sessionID, agent, mode, modes
	})
	return nil
}

// withAuth runs fn; when the agent asks for authentication and offers an
// API key method (the gateway key is in the environment), it
// authenticates and runs fn again.
func (b *Bridge) withAuth(ctx context.Context, init initResult, fn func() error) error {
	err := fn()
	var re *RPCError
	if !errors.As(err, &re) || re.Code != -32000 {
		return err
	}
	for _, m := range init.AuthMethods {
		id := strings.ToLower(m.ID)
		if m.Type == "terminal" || !(strings.Contains(id, "key") || strings.Contains(id, "env") || strings.Contains(id, "gemini")) {
			continue
		}
		if b.conn.Call(ctx, "authenticate", map[string]string{"methodId": m.ID}, nil) == nil {
			return fn()
		}
	}
	return err
}

func (b *Bridge) fullAccess(modes []harness.AgentMode) string {
	a, err := AgentFor(b.opts.Harness, "")
	if err != nil {
		return ""
	}
	for _, want := range a.FullAccess {
		for _, m := range modes {
			if m.ID == want {
				return want
			}
		}
	}
	return ""
}

// setModel selects the model of the session when the agent lists it as a
// model config option (as "model", "lydiabox/model", or ".../model").
func (b *Bridge) setModel(ctx context.Context, opts []configOption) {
	if b.opts.Model == "" {
		return
	}
	for _, o := range opts {
		if o.Category != "model" && o.ID != "model" {
			continue
		}
		for _, v := range o.Options {
			if v.Value == b.opts.Model || v.Value == "lydiabox/"+b.opts.Model || strings.HasSuffix(v.Value, "/"+b.opts.Model) {
				_ = b.conn.Call(ctx, "session/set_config_option", map[string]any{"sessionId": b.session, "configId": o.ID, "value": v.Value}, nil)
				return
			}
		}
	}
}

// history sends the transcript events of a session that the agent resumes
// without a replay.
func (b *Bridge) history(id string) {
	if b.opts.History == nil {
		return
	}
	evs, err := b.opts.History.Events(b.opts.Home, b.opts.Harness, id)
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, e := range evs {
		if e.Kind == harness.EventUser {
			b.ev.turn++
		}
		b.ev.put(e)
	}
}

func (b *Bridge) closeSession() {
	b.mu.Lock()
	id := b.session
	b.mu.Unlock()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = b.conn.Call(ctx, "session/close", map[string]string{"sessionId": id}, nil)
}

// readHost serves the host commands; the end of the input ends the run.
func (b *Bridge) readHost(ctx context.Context, stop context.CancelFunc) {
	defer stop()
	sc := bufio.NewScanner(b.opts.In)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var c harness.BridgeCommand
		if json.Unmarshal(sc.Bytes(), &c) != nil {
			continue
		}
		b.command(ctx, c)
	}
}

func (b *Bridge) command(ctx context.Context, c harness.BridgeCommand) {
	switch c.Type {
	case harness.BridgePrompt:
		if strings.TrimSpace(c.Text) == "" {
			return
		}
		b.mu.Lock()
		b.queued++
		b.setStatusLocked(func(*harness.AgentStatus) {})
		b.mu.Unlock()
		b.prompts <- c.Text
	case harness.BridgeCancel:
		b.cancel()
	case harness.BridgePermission:
		b.mu.Lock()
		ch := b.waiting[c.RequestID]
		b.mu.Unlock()
		if ch != nil {
			select {
			case ch <- c.OptionID:
			default:
			}
		}
	case harness.BridgeSync:
		b.mu.Lock()
		b.write(harness.BridgeMessage{Type: harness.BridgeReset})
		for _, e := range b.ev.list {
			b.emitEvent(e)
		}
		b.setStatusLocked(func(*harness.AgentStatus) {})
		b.mu.Unlock()
	case harness.BridgeMode:
		b.mu.Lock()
		id := b.session
		b.mu.Unlock()
		if id != "" && c.ModeID != "" && b.conn.Call(ctx, "session/set_mode", map[string]string{"sessionId": id, "modeId": c.ModeID}, nil) == nil {
			b.setStatus(func(s *harness.AgentStatus) { s.Mode = c.ModeID })
		}
	}
}

// cancel stops the current turn, drops the queued prompts, and answers the
// open permission requests with "cancelled".
func (b *Bridge) cancel() {
	b.mu.Lock()
	for len(b.prompts) > 0 {
		<-b.prompts
		b.queued--
	}
	for id, ch := range b.waiting {
		select {
		case ch <- "":
		default:
		}
		delete(b.waiting, id)
	}
	id, working := b.session, b.working
	b.setStatusLocked(func(*harness.AgentStatus) {})
	b.mu.Unlock()
	if id != "" && working {
		_ = b.conn.Notify("session/cancel", map[string]string{"sessionId": id})
	}
}

// runPrompts sends the queued prompts one after the other.
func (b *Bridge) runPrompts(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-b.prompts:
			b.prompt(ctx, text)
		}
	}
}

type promptResult struct {
	StopReason string `json:"stopReason"`
	Usage      *struct {
		InputTokens       int64 `json:"inputTokens"`
		OutputTokens      int64 `json:"outputTokens"`
		CachedReadTokens  int64 `json:"cachedReadTokens"`
		CachedWriteTokens int64 `json:"cachedWriteTokens"`
		ThoughtTokens     int64 `json:"thoughtTokens"`
	} `json:"usage"`
}

func (b *Bridge) prompt(ctx context.Context, text string) {
	b.mu.Lock()
	b.queued--
	b.working = true
	b.ev.user(text)
	id := b.session
	b.setStatusLocked(func(s *harness.AgentStatus) { s.State, s.Error = harness.AgentWorking, "" })
	b.mu.Unlock()
	var res promptResult
	err := b.conn.Call(ctx, "session/prompt", map[string]any{"sessionId": id, "prompt": []map[string]string{{"type": "text", "text": text}}}, &res)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.working = false
	switch {
	case err != nil && ctx.Err() != nil:
		// The bridge stops (the session switches engine or ends).
		res.StopReason = "cancelled"
	case err != nil:
		b.ev.errorText(err.Error())
		res.StopReason = "error"
	}
	if u := res.Usage; u != nil {
		b.ev.usage("", func(t *harness.TurnUsage) {
			t.InputTokens = u.InputTokens + u.CachedReadTokens + u.CachedWriteTokens
			t.OutputTokens, t.CachedTokens, t.ReasoningTokens = u.OutputTokens, u.CachedReadTokens, u.ThoughtTokens
		})
	}
	b.ev.turnEnd(res.StopReason)
	state := harness.AgentIdle
	if b.queued > 0 {
		state = harness.AgentWorking
	}
	b.setStatusLocked(func(s *harness.AgentStatus) { s.State = state })
}

// handle serves the requests and notifications of the agent.
func (b *Bridge) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session/update":
		b.update(params)
		return nil, nil
	case "session/request_permission":
		return b.permission(ctx, params)
	}
	return nil, &RPCError{Code: CodeMethodNotFound, Message: "method not found: " + method}
}

// permission shows a permission request to the user and waits for the
// answer (or a cancel).
func (b *Bridge) permission(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		ToolCall toolUpdate `json:"toolCall"`
		Options  []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &RPCError{Code: -32602, Message: "invalid params"}
	}
	ch := make(chan string, 1)
	b.mu.Lock()
	reqID := b.ev.key("perm")
	b.waiting[reqID] = ch
	if p.ToolCall.ToolCallID != "" {
		b.ev.tool(p.ToolCall)
	}
	title := ""
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
	perm := &harness.Permission{ID: reqID, Title: title, ToolCallID: p.ToolCall.ToolCallID}
	for _, o := range p.Options {
		perm.Options = append(perm.Options, harness.PermissionOption{ID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	b.ev.put(harness.Event{Key: reqID, Kind: harness.EventPermission, Time: b.ev.now().UTC(), Permission: perm})
	b.setStatusLocked(func(s *harness.AgentStatus) { s.State = harness.AgentAttention })
	b.mu.Unlock()

	var option string
	select {
	case option = <-ch:
	case <-ctx.Done():
	}
	b.mu.Lock()
	delete(b.waiting, reqID)
	answered := *perm
	answered.Outcome = option
	if option == "" {
		answered.Outcome = "cancelled"
	}
	b.ev.put(harness.Event{Key: reqID, Kind: harness.EventPermission, Time: b.ev.now().UTC(), Permission: &answered})
	if len(b.waiting) == 0 && b.working {
		b.setStatusLocked(func(s *harness.AgentStatus) { s.State = harness.AgentWorking })
	}
	b.mu.Unlock()
	if option == "" {
		return map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}, nil
	}
	return map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option}}, nil
}
