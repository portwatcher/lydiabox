//go:build unix

package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"

	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	"github.com/portwatcher/lydiabox/internal/vmm"
	"github.com/portwatcher/lydiabox/internal/vmm/guestclient"
)

func readUntil(t *testing.T, p guestclient.PTY, want string) {
	t.Helper()
	var buf bytes.Buffer
	b := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %q; got %q", want, buf.String())
		}
		n, err := p.Read(b)
		buf.Write(b[:n])
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, buf.String())
		}
	}
}

func TestLinkCarriesRPCPTYAndReverseChannels(t *testing.T) {
	checkLeaks(t)
	r := newRig(t, rigOptions{})
	serveHost(t, r.link, rpc.PortHostBroker, "broker")
	serveHost(t, r.link, rpc.PortHostGateway, "gateway")
	ctx := context.Background()

	// The first call waits for the link to wake.
	if _, err := r.client.Ping(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if st := r.link.Status(); st.State != StateUp || st.Connects != 1 {
		t.Fatalf("status %+v", st)
	}
	p, err := r.client.StartPTY(ctx, "default", guestclient.PTYSpec{ID: "sh", Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Write([]byte("echo link=$((40+2))\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "link=42")
	if _, err := p.Write([]byte("exit 0\n")); err != nil {
		t.Fatal(err)
	}
	if code, err := p.Wait(ctx); err != nil || code != 0 {
		t.Fatalf("wait %d %v", code, err)
	}
	p.Close()

	if got := guestGet(t, r.guest, rpc.PortHostBroker); got != "broker" {
		t.Fatalf("broker answered %q", got)
	}
	if got := guestGet(t, r.guest, rpc.PortHostGateway); got != "gateway" {
		t.Fatalf("gateway answered %q", got)
	}
	// A port without a host listener is refused, and the link stays up.
	c, err := r.guest.Dial(ctx, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("stream to an unknown host port stayed open")
	}
	c.Close()
	if _, err := r.client.Ping(ctx, "default"); err != nil {
		t.Fatal(err)
	}
}

func TestLinkCarriesDesktopSignalStream(t *testing.T) {
	r := newRig(t, rigOptions{desktop: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ss, err := r.client.OpenSignal(ctx, "default", rpc.SignalParams{})
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	if ss.Hello.Screen.Width != 1280 {
		t.Fatalf("hello %+v", ss.Hello)
	}
	browser, err := pion.NewPeerConnection(pion.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer browser.Close()
	if _, err := browser.AddTransceiverFromKind(pion.RTPCodecTypeVideo, pion.RTPTransceiverInit{Direction: pion.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := browser.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := json.Marshal(map[string]string{"type": "offer", "sdp": offer.SDP})
	if err := ss.Send(msg); err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := ss.Recv()
		if err != nil {
			t.Fatal(err)
		}
		var m struct{ Type, SDP string }
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if m.Type == "error" {
			t.Fatalf("guest error %s", raw)
		}
		if m.Type == "answer" {
			if !strings.Contains(m.SDP, "m=video") {
				t.Fatalf("answer %s", m.SDP)
			}
			return
		}
	}
}

func TestLinkReconnectsAfterProxyDrop(t *testing.T) {
	checkLeaks(t)
	r := newRig(t, rigOptions{})
	serveHost(t, r.link, rpc.PortHostBroker, "broker")
	ctx := context.Background()
	if _, err := r.client.Ping(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	r.proxy.drop()
	// Calls while the link is down fail at once.
	deadline := time.Now().Add(10 * time.Second)
	for {
		start := time.Now()
		_, err := r.link.Dial(ctx, rpc.PortGuestRPC)
		if errors.Is(err, vmm.ErrUnavailable) {
			if d := time.Since(start); d > time.Second {
				t.Fatalf("dial while down took %s", d)
			}
			break
		}
		if err == nil && time.Now().After(deadline) {
			t.Fatal("the link did not notice the dropped connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
	deadline = time.Now().Add(15 * time.Second)
	for st := r.link.Status(); st.State != StateUp || st.Connects < 2; st = r.link.Status() {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The guest client redials over the new link; the host listener
	// survived the reconnect.
	var err error
	for i := 0; i < 50; i++ {
		if _, err = r.client.Ping(ctx, "default"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if got := guestGet(t, r.guest, rpc.PortHostBroker); got != "broker" {
		t.Fatalf("broker answered %q", got)
	}
}

func TestLinkRefusesWrongSecret(t *testing.T) {
	checkLeaks(t)
	r := newRig(t, rigOptions{hostSecret: []byte("another-secret-of-enough-length")})
	start := time.Now()
	_, err := r.link.Dial(context.Background(), rpc.PortGuestRPC)
	if !errors.Is(err, vmm.ErrUnavailable) || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("dial with the wrong secret: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("refusal took %s", d)
	}
	if r.guest.Connected() {
		t.Fatal("the guest accepted a host with the wrong secret")
	}
}

func TestLinkRefusesWrongToken(t *testing.T) {
	checkLeaks(t)
	r := newRig(t, rigOptions{token: "ibp_wrong"})
	_, err := r.link.Dial(context.Background(), rpc.PortGuestRPC)
	if !errors.Is(err, vmm.ErrUnavailable) || !strings.Contains(err.Error(), "401") {
		t.Fatalf("dial with the wrong token: %v", err)
	}
	if r.guest.Connected() {
		t.Fatal("the guest accepted a link without the proxy token")
	}
}

func TestFirstDialWaitsForSlowWakeAndSuspendStopsTheLink(t *testing.T) {
	checkLeaks(t)
	r := newRig(t, rigOptions{upgradeDelay: 700 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.client.Ping(ctx, "default"); err != nil {
		t.Fatalf("first call during a slow wake: %v", err)
	}
	r.link.Suspend()
	if st := r.link.Status(); st.State != StateIdle {
		t.Fatalf("after suspend %+v", st)
	}
	start := time.Now()
	if _, err := r.link.Dial(ctx, rpc.PortGuestRPC); !errors.Is(err, vmm.ErrUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("dial while suspended: %v after %s", err, time.Since(start))
	}
	// No reconnect happens while suspended.
	time.Sleep(300 * time.Millisecond)
	if st := r.link.Status(); st.State != StateIdle || st.Connects != 1 {
		t.Fatalf("suspended link changed: %+v", st)
	}
	if err := r.link.Wake(); err != nil {
		t.Fatal(err)
	}
	r.client.Reset("default")
	if _, err := r.client.Ping(ctx, "default"); err != nil {
		t.Fatalf("call after wake: %v", err)
	}
}

func TestSlowStreamDoesNotBlockOtherStreams(t *testing.T) {
	checkLeaks(t)
	r := newRig(t, rigOptions{})
	ln, err := r.link.Listen(rpc.PortHostBroker)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	waitState(t, r.link, StateUp)

	slow, err := r.guest.Dial(ctx, rpc.PortHostBroker)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	slowHost, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer slowHost.Close()
	// Nobody reads the slow stream; its writer stops at the window.
	wrote := make(chan error, 1)
	go func() {
		_, err := slow.Write(make([]byte, 8<<20))
		wrote <- err
	}()
	time.Sleep(200 * time.Millisecond)

	fast, err := r.guest.Dial(ctx, rpc.PortHostBroker)
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	fastHost, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer fastHost.Close()
	go func() { _, _ = io.Copy(fastHost, fastHost) }()
	payload := bytes.Repeat([]byte("x"), 256<<10)
	go func() { _, _ = fast.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(fast, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("echo on the fast stream: %v", err)
	}
	select {
	case err := <-wrote:
		t.Fatalf("the slow write finished without a reader: %v", err)
	default:
	}
	// Draining the slow stream lets its writer finish.
	go func() { _, _ = io.Copy(io.Discard, slowHost) }()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the slow write did not finish after the reader started")
	}
}
