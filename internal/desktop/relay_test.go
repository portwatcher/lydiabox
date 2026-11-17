package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

type fakeSignal struct {
	fromBrowser chan json.RawMessage
	toBrowser   chan json.RawMessage
	closed      chan struct{}
	once        sync.Once
}

func (s *fakeSignal) Send(m json.RawMessage) error {
	s.fromBrowser <- m
	return nil
}

func (s *fakeSignal) Recv() (json.RawMessage, error) {
	select {
	case m := <-s.toBrowser:
		return m, nil
	case <-s.closed:
		return nil, io.EOF
	}
}

func (s *fakeSignal) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

type fakeEvents struct {
	ch     chan json.RawMessage
	closed chan struct{}
	once   sync.Once
}

func (e *fakeEvents) Recv() (json.RawMessage, error) {
	select {
	case m := <-e.ch:
		return m, nil
	case <-e.closed:
		return nil, io.EOF
	}
}

func (e *fakeEvents) Close() error {
	e.once.Do(func() { close(e.closed) })
	return nil
}

type fakeGuest struct {
	mu       sync.Mutex
	signals  []*fakeSignal
	events   chan json.RawMessage
	mode     string
	launched []string
	publicIP string
	params   []rpc.SignalParams
}

func (g *fakeGuest) OpenSignal(_ context.Context, _ string, p rpc.SignalParams) (SignalConn, rpc.SignalHello, error) {
	s := &fakeSignal{fromBrowser: make(chan json.RawMessage, 8), toBrowser: make(chan json.RawMessage, 8), closed: make(chan struct{})}
	g.mu.Lock()
	g.signals = append(g.signals, s)
	g.publicIP = p.PublicIP
	g.params = append(g.params, p)
	g.mu.Unlock()
	return s, rpc.SignalHello{Mode: rpc.ModeObserver, Screen: rpc.Screen{Width: 1920, Height: 1080}}, nil
}

func (g *fakeGuest) SubscribeEvents(context.Context, string) (EventConn, error) {
	return &fakeEvents{ch: g.events, closed: make(chan struct{})}, nil
}

func (g *fakeGuest) GetMode(context.Context, string) (rpc.ModeState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return rpc.ModeState{Mode: g.mode, ChangedBy: "system"}, nil
}

func (g *fakeGuest) SetMode(_ context.Context, _ string, mode, by string) (rpc.ModeState, error) {
	if mode != rpc.ModeObserver && mode != rpc.ModeTakeover {
		return rpc.ModeState{}, rpc.Errorf(rpc.CodeBadRequest, "invalid mode")
	}
	g.mu.Lock()
	g.mode = mode
	g.mu.Unlock()
	b, _ := json.Marshal(map[string]string{"type": "mode", "mode": mode, "changedBy": by})
	g.events <- b
	return rpc.ModeState{Mode: mode, ChangedBy: by}, nil
}

func (g *fakeGuest) LaunchApp(_ context.Context, _ string, app string) error {
	if app != "chrome" {
		return rpc.Errorf(rpc.CodeBadRequest, "unknown app")
	}
	g.mu.Lock()
	g.launched = append(g.launched, app)
	g.mu.Unlock()
	return nil
}

type fakePower struct {
	mu      sync.Mutex
	leases  int
	wakes   int
	running bool
	wakeErr error
}

func (p *fakePower) EnsureRunning(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wakes++
	if p.wakeErr != nil {
		return p.wakeErr
	}
	p.running = true
	return nil
}

func (p *fakePower) AcquireLease(string, string) func() {
	p.mu.Lock()
	p.leases++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.leases--
			p.mu.Unlock()
		})
	}
}

func (p *fakePower) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leases
}

func setup(t *testing.T) (*httptest.Server, *fakeGuest, *fakePower, *Relay) {
	t.Helper()
	return setupWith(t, nil)
}

// setupWith is setup with Options changed by edit.
func setupWith(t *testing.T, edit func(*Options)) (*httptest.Server, *fakeGuest, *fakePower, *Relay) {
	t.Helper()
	g := &fakeGuest{events: make(chan json.RawMessage, 16), mode: rpc.ModeObserver}
	p := &fakePower{running: true}
	opts := Options{Guest: g, Power: p, Logger: slog.New(slog.DiscardHandler),
		Running:  func(string) bool { p.mu.Lock(); defer p.mu.Unlock(); return p.running },
		PublicIP: func(r *http.Request) string { return RequestIP(r, true) }}
	if edit != nil {
		edit(&opts)
	}
	rl, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/desktop/ws", rl.ServeWS)
	mux.HandleFunc("GET /api/v1/desktop/mode", rl.ServeMode)
	mux.HandleFunc("PUT /api/v1/desktop/mode", rl.ServeMode)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(tenant.WithID(r.Context(), "default")))
	}))
	t.Cleanup(srv.Close)
	return srv, g, p, rl
}

func dial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/desktop/ws",
		&websocket.DialOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func readMsg(t *testing.T, c *websocket.Conn, wantType string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %s: %v", wantType, err)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if m["type"] == wantType {
			return m
		}
	}
}

func write(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(msg)); err != nil {
		t.Fatal(err)
	}
}

func TestSignalingRelayAndBroadcast(t *testing.T) {
	srv, g, p, rl := setup(t)
	a := dial(t, srv)
	hello := readMsg(t, a, "hello")
	if hello["mode"] != "OBSERVER" || hello["screen"].(map[string]any)["width"].(float64) != 1920 || hello["iceServers"] == nil {
		t.Fatalf("hello %v", hello)
	}
	if v := readMsg(t, a, "viewers"); v["count"].(float64) != 1 {
		t.Fatalf("viewers %v", v)
	}
	if p.count() != 1 || g.publicIP != "127.0.0.1" {
		t.Fatalf("leases %d publicIP %q", p.count(), g.publicIP)
	}

	// offer and ice go to the guest; answer and ice come back.
	write(t, a, `{"type":"offer","sdp":"v=0 offer"}`)
	g.mu.Lock()
	sig := g.signals[0]
	g.mu.Unlock()
	select {
	case m := <-sig.fromBrowser:
		if !strings.Contains(string(m), "v=0 offer") {
			t.Fatalf("relayed %s", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("offer not relayed")
	}
	sig.toBrowser <- json.RawMessage(`{"type":"answer","sdp":"v=0 answer"}`)
	if m := readMsg(t, a, "answer"); m["sdp"] != "v=0 answer" {
		t.Fatalf("answer %v", m)
	}
	write(t, a, `{"type":"ice","candidate":{"candidate":"candidate:1","sdpMid":"0","sdpMLineIndex":0}}`)
	if m := <-sig.fromBrowser; !strings.Contains(string(m), "candidate:1") {
		t.Fatalf("ice %s", m)
	}

	b := dial(t, srv)
	readMsg(t, b, "hello")
	if v := readMsg(t, a, "viewers"); v["count"].(float64) != 2 {
		t.Fatalf("viewers %v", v)
	}

	// A mode change by one browser reaches every browser.
	write(t, b, `{"type":"mode","mode":"TAKEOVER"}`)
	for _, c := range []*websocket.Conn{a, b} {
		if m := readMsg(t, c, "mode"); m["mode"] != "TAKEOVER" || m["changedBy"] != "user" {
			t.Fatalf("mode %v", m)
		}
	}
	g.events <- json.RawMessage(`{"type":"agent_action","action":"click","x":812,"y":440}`)
	for _, c := range []*websocket.Conn{a, b} {
		if m := readMsg(t, c, "agent_action"); m["x"].(float64) != 812 {
			t.Fatalf("agent_action %v", m)
		}
	}
	write(t, a, `{"type":"ping","id":3}`)
	if m := readMsg(t, a, "pong"); m["id"].(float64) != 3 {
		t.Fatalf("pong %v", m)
	}
	write(t, a, `{"type":"launch","app":"chrome"}`)
	write(t, a, `{"type":"launch","app":"doom"}`)
	if m := readMsg(t, a, "error"); m["code"] != "bad_request" {
		t.Fatalf("launch error %v", m)
	}

	// Mode routes.
	resp, err := http.Get(srv.URL + "/api/v1/desktop/mode")
	if err != nil {
		t.Fatal(err)
	}
	var st rpc.ModeState
	_ = json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.Mode != "TAKEOVER" {
		t.Fatalf("GET mode %+v", st)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/desktop/mode", strings.NewReader(`{"mode":"OBSERVER"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("PUT mode %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/api/v1/desktop/mode", strings.NewReader(`{"mode":"BOGUS"}`))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("PUT bad mode %d", resp.StatusCode)
	}

	b.Close(websocket.StatusNormalClosure, "")
	if v := readMsg(t, a, "viewers"); v["count"].(float64) != 1 {
		t.Fatalf("viewers after leave %v", v)
	}
	a.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for p.count() != 0 || rl.Viewers("default") != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("leases %d viewers %d", p.count(), rl.Viewers("default"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-sig.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("guest signal stream not closed")
	}
}

func TestRelayRejectsAndReportsFailures(t *testing.T) {
	srv, _, p, _ := setup(t)
	p.mu.Lock()
	p.running = false
	p.mu.Unlock()
	resp, err := http.Get(srv.URL + "/api/v1/desktop/mode")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("mode while stopped: %d", resp.StatusCode)
	}

	// Without the subprotocol the server closes with a policy violation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/desktop/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close status %v", err)
	}

	// A socket for a VM that does not run does not start it.
	if m := readMsg(t, dial(t, srv), "error"); m["code"] != CodeStopped {
		t.Fatalf("socket while stopped %v", m)
	}
	p.mu.Lock()
	wakes := p.wakes
	p.running = true
	p.wakeErr = errors.New("guest not ready")
	p.mu.Unlock()
	if wakes != 0 {
		t.Fatalf("%d wakes for sockets to a stopped VM", wakes)
	}
	w := dial(t, srv)
	if m := readMsg(t, w, "error"); m["code"] != "unavailable" || !strings.Contains(m["message"].(string), "guest not ready") {
		t.Fatalf("wake error %v", m)
	}
}
