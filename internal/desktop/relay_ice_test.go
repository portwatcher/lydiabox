package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/desktop/ice"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// fakeICE mints numbered credentials that expire after ttl.
type fakeICE struct {
	mu       sync.Mutex
	ttl      time.Duration
	err      error
	minted   int
	released []string
}

func (f *fakeICE) Grant(context.Context, string) (ice.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return ice.Grant{}, f.err
	}
	f.minted++
	user := "user" + string(rune('0'+f.minted))
	return ice.NewGrant([]rpc.ICEServer{{URLs: []string{"turn:turn.example.com:3478?transport=tcp"}, Username: user, Credential: "pw"}},
		time.Now().Add(f.ttl), func() {
			f.mu.Lock()
			f.released = append(f.released, user)
			f.mu.Unlock()
		}), nil
}

func (f *fakeICE) releasedUsers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

func (g *fakeGuest) paramsAt(i int) rpc.SignalParams {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.params[i]
}

func (g *fakeGuest) signalCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.signals)
}

func TestDefaultICEUnchanged(t *testing.T) {
	srv, g, _, _ := setup(t)
	a := dial(t, srv)
	hello := readMsg(t, a, "hello")
	b, _ := json.Marshal(hello["iceServers"])
	if string(b) != `[{"urls":["stun:stun.l.google.com:19302"]}]` || hello["iceTransportPolicy"] != "all" {
		t.Fatalf("hello %v", hello)
	}
	p := g.paramsAt(0)
	if p.PublicIP != "127.0.0.1" || p.ICEServers != nil || p.ICETransportPolicy != "" {
		t.Fatalf("guest params %+v", p)
	}
}

func TestRelayOnlyGivesTURNToBothPeers(t *testing.T) {
	f := &fakeICE{ttl: time.Hour}
	srv, g, _, _ := setupWith(t, func(o *Options) { o.ICE = f; o.RelayOnly = true })
	a := dial(t, srv)
	hello := readMsg(t, a, "hello")
	servers := hello["iceServers"].([]any)
	s0 := servers[0].(map[string]any)
	if hello["iceTransportPolicy"] != "relay" || len(servers) != 1 || s0["username"] != "user1" || s0["credential"] != "pw" {
		t.Fatalf("hello %v", hello)
	}
	p := g.paramsAt(0)
	if p.ICETransportPolicy != rpc.ICEPolicyRelay || p.PublicIP != "" || len(p.ICEServers) != 1 || p.ICEServers[0].Username != "user1" {
		t.Fatalf("guest params %+v", p)
	}
	a.CloseNow()
	waitFor(t, func() bool { return len(f.releasedUsers()) == 1 }, "credentials released on close")
}

func TestRelayOnlyFailsWithoutTURNCredentials(t *testing.T) {
	f := &fakeICE{err: errors.New("mint failed")}
	srv, g, _, _ := setupWith(t, func(o *Options) { o.ICE = f; o.RelayOnly = true })
	a := dial(t, srv)
	if m := readMsg(t, a, "error"); m["code"] != "unavailable" || !strings.Contains(m["message"].(string), "mint failed") {
		t.Fatalf("error %v", m)
	}
	if g.signalCount() != 0 {
		t.Fatal("guest peer opened without credentials")
	}
}

func TestICEFailureFallsBackOutsideRelayOnly(t *testing.T) {
	f := &fakeICE{err: errors.New("mint failed")}
	srv, g, _, _ := setupWith(t, func(o *Options) { o.ICE = f })
	a := dial(t, srv)
	hello := readMsg(t, a, "hello")
	b, _ := json.Marshal(hello["iceServers"])
	if string(b) != `[{"urls":["stun:stun.l.google.com:19302"]}]` || hello["iceTransportPolicy"] != "all" {
		t.Fatalf("hello %v", hello)
	}
	if p := g.paramsAt(0); p.ICEServers != nil || p.PublicIP == "" {
		t.Fatalf("guest params %+v", p)
	}
}

func TestCredentialsRefreshBeforeExpiry(t *testing.T) {
	// refreshLead(1s) is 200 ms: the new session starts after about 800 ms.
	f := &fakeICE{ttl: time.Second}
	srv, g, _, _ := setupWith(t, func(o *Options) { o.ICE = f })
	a := dial(t, srv)
	if h := readMsg(t, a, "hello"); h["iceServers"].([]any)[0].(map[string]any)["username"] != "user1" {
		t.Fatalf("hello %v", h)
	}
	h2 := readMsg(t, a, "hello")
	if h2["iceServers"].([]any)[0].(map[string]any)["username"] != "user2" {
		t.Fatalf("second hello %v", h2)
	}
	if g.signalCount() != 2 || g.paramsAt(1).ICEServers[0].Username != "user2" {
		t.Fatalf("guest sessions %d", g.signalCount())
	}
	g.mu.Lock()
	first, second := g.signals[0], g.signals[1]
	g.mu.Unlock()
	select {
	case <-first.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("old guest session not closed")
	}
	waitFor(t, func() bool { u := f.releasedUsers(); return len(u) == 1 && u[0] == "user1" }, "old credentials released")

	// Browser messages go to the new session; the socket stays open.
	write(t, a, `{"type":"offer","sdp":"v=0 renewed"}`)
	select {
	case m := <-second.fromBrowser:
		if !strings.Contains(string(m), "renewed") {
			t.Fatalf("relayed %s", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("offer not relayed to the new session")
	}
	second.toBrowser <- json.RawMessage(`{"type":"answer","sdp":"v=0 answer2"}`)
	if m := readMsg(t, a, "answer"); m["sdp"] != "v=0 answer2" {
		t.Fatalf("answer %v", m)
	}
}

func TestRelayOnlyNeedsProvider(t *testing.T) {
	if _, err := New(Options{Guest: &fakeGuest{}, Power: &fakePower{}, RelayOnly: true}); err == nil {
		t.Fatal("RelayOnly without ICE accepted")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
