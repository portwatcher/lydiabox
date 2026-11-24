//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	pion "github.com/pion/webrtc/v4"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/desktop"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
)

// TestInstaboxCellEndToEnd runs the real `lydiabox up` wiring as a cell
// (UNIX socket, secrets from the control plane) in the provider storage
// mode, with the driver selected by "auto", against the fake InstaBox
// API. The machine runs the real guest daemon behind the fake's private
// port proxy.
func TestInstaboxCellEndToEnd(t *testing.T) {
	checkGoroutines(t)
	r := newCellRig(t)
	// This test covers the host gateway; TestCellGuestNative covers the
	// default of a cell.
	r.env = append(r.env, "LYDIABOX_GUEST_NATIVE=false")
	c := r.start()

	// Nothing wakes or makes the machine at start.
	if st, _ := r.vmState(); st != api.VMStopped || len(r.fake.Machines()) != 0 {
		t.Fatalf("state at start %s, %d machines", st, len(r.fake.Machines()))
	}
	var status struct{ VM struct{ Driver string } }
	if err := json.Unmarshal(r.must(http.MethodGet, "/api/v1/status", "", http.StatusOK), &status); err != nil || status.VM.Driver != "instabox" {
		t.Fatalf("driver %q (%v): auto must select instabox", status.VM.Driver, err)
	}

	// Wake on demand: a file operation makes and boots the machine.
	r.must(http.MethodPost, "/api/v1/fs/workspace/mkdir", `{"path":"notes","parents":true}`, http.StatusCreated)
	r.must(http.MethodPut, "/api/v1/fs/workspace/text?path=notes/hello.txt", `{"content":"hello from the host"}`, http.StatusOK)
	id := r.machine()
	r.waitVM(api.VMRunning)
	data, err := os.ReadFile(filepath.Join(r.runner.DataDir(id), "workspace", "notes", "hello.txt"))
	if err != nil || string(data) != "hello from the host" {
		t.Fatalf("file on the machine disk: %q %v", data, err)
	}
	if b := r.must(http.MethodGet, "/api/v1/fs/workspace/list?path=notes", "", http.StatusOK); !strings.Contains(string(b), "hello.txt") {
		t.Fatalf("list %s", b)
	}

	t.Run("pty session", func(t *testing.T) { ptySession(t, r) })
	t.Run("desktop signaling", func(t *testing.T) { desktopSignal(t, r) })
	t.Run("reverse channels", func(t *testing.T) {
		code, body := r.guestHTTP(id, int(rpc.PortHostGateway), http.MethodGet, "/v1/models")
		if code != http.StatusOK || !strings.Contains(body, `"object":"list"`) {
			t.Fatalf("LLM gateway from the guest: %d %s", code, body)
		}
		if p := r.upstream.paths(); len(p) == 0 || p[len(p)-1] != "/v1/models" {
			t.Fatalf("upstream got %v", p)
		}
		code, body = r.guestHTTP(id, int(rpc.PortHostBroker), http.MethodGet, "/mcp")
		if code != http.StatusMethodNotAllowed || !strings.Contains(body, "connector MCP server") {
			t.Fatalf("connector broker from the guest: %d %s", code, body)
		}
	})

	// Idle hibernate: without leases the cell pauses the machine, and the
	// next use resumes it with its memory.
	r.waitVM(api.VMPaused)
	if st := r.fake.State(id); st != "suspended" {
		t.Fatalf("machine %s after the idle timeout", st)
	}
	if b := r.must(http.MethodGet, "/api/v1/fs/workspace/text?path=notes/hello.txt", "", http.StatusOK); !strings.Contains(string(b), "hello from the host") {
		t.Fatalf("read after the wake %s", b)
	}
	if st, _ := r.vmState(); st != api.VMRunning || r.runner.Boots(id) != 1 {
		t.Fatalf("state %s, %d guest boots after the resume", st, r.runner.Boots(id))
	}

	// A pause by InstaBox itself while the link is open: the watcher reports
	// it, and the next use wakes the machine.
	wakes := r.fake.Wakes()
	if !r.fake.IdlePause(id) {
		t.Fatal("idle pause")
	}
	r.waitVM(api.VMPaused)
	r.must(http.MethodGet, "/api/v1/fs/workspace/list?path=notes", "", http.StatusOK)
	if r.fake.Wakes() <= wakes || r.fake.State(id) != "running" {
		t.Fatalf("the self-paused machine was not woken (%d wakes, %s)", r.fake.Wakes(), r.fake.State(id))
	}

	// A restart of the host process adopts the same machine.
	c.stop(t)
	if st := r.fake.State(id); st != "running" && st != "suspended" {
		t.Fatalf("the cell stop changed the machine to %s", st)
	}
	c = r.start()
	if b := r.must(http.MethodGet, "/api/v1/fs/workspace/text?path=notes/hello.txt", "", http.StatusOK); !strings.Contains(string(b), "hello from the host") {
		t.Fatalf("read after the restart %s", b)
	}
	if got := r.machine(); got != id {
		t.Fatalf("machine %s after the restart, want %s", got, id)
	}
	creates, deletes := 0, 0
	for _, q := range r.fake.Requests() {
		switch {
		case q.Method == http.MethodPost && q.Path == "/v1/machines":
			creates++
		case q.Method == http.MethodDelete && strings.HasPrefix(q.Path, "/v1/machines/") && !strings.Contains(q.Path, "/activity-leases/"):
			deletes++
		}
		for _, v := range []string{q.Header.Get("Authorization"), string(q.Body)} {
			if strings.Contains(v, cellToken) {
				t.Fatalf("the admin token reached InstaBox in %s %s", q.Method, q.Path)
			}
		}
	}
	if creates != 1 || deletes != 0 {
		t.Fatalf("%d creates and %d deletes, want 1 and 0", creates, deletes)
	}
	envFile, _, _ := r.fake.File(id, "/data/.lydiabox/guest.env")
	if strings.Contains(string(envFile), r.fake.APIKey()) {
		t.Fatal("the InstaBox API key is on the guest disk")
	}
	c.stop(t)
}

// ptySession runs a shell session through the sessions API and its
// terminal WebSocket.
func ptySession(t *testing.T, r *cellRig) {
	b := r.must(http.MethodPost, "/api/v1/sessions", `{"harness":"shell","workspaceDir":"/workspace/notes"}`, http.StatusCreated)
	var sess struct{ ID string }
	if err := json.Unmarshal(b, &sess); err != nil || sess.ID == "" {
		t.Fatalf("session %s", b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws := r.dialWS(ctx, "/api/v1/sessions/"+sess.ID+"/ws", api.PTYSubprotocol)
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageBinary, []byte("echo pty=$((40+2)) in $(basename $PWD)\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), "pty=42 in notes") {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("terminal output %q: %v", out.String(), err)
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
		}
	}
	if st, _ := r.vmState(); st != api.VMRunning {
		t.Fatalf("state with a terminal %s", st)
	}
	ws.Close(websocket.StatusNormalClosure, "")
	r.must(http.MethodDelete, "/api/v1/sessions/"+sess.ID, "", http.StatusNoContent)
}

// desktopSignal opens the desktop signaling socket and gets an answer to a
// browser offer from the guest's WebRTC peer.
func desktopSignal(t *testing.T, r *cellRig) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws := r.dialWS(ctx, "/api/v1/desktop/ws", desktop.Subprotocol)
	defer ws.CloseNow()
	read := func(want string) map[string]any {
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				t.Fatalf("signaling: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			if m["type"] == "error" {
				t.Fatalf("signaling error %s", data)
			}
			if m["type"] == want {
				return m
			}
		}
	}
	hello := read("hello")
	if hello["iceTransportPolicy"] != "relay" {
		t.Fatalf("hello %v: the desktop must relay through TURN", hello)
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
	if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
	if answer := read("answer"); !strings.Contains(answer["sdp"].(string), "m=video") {
		t.Fatalf("answer %v", answer)
	}
	ws.Close(websocket.StatusNormalClosure, "")
}
