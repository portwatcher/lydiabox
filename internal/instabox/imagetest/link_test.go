package imagetest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/vmm/link"
)

// TestImageLink connects to the guest daemon of the image the way the
// instabox driver will (a host-dialed WebSocket link with the shared
// secret), calls it, and checks the reverse bridge from the guest's
// broker socket to host port 9001.
func TestImageLink(t *testing.T) {
	linkURL := os.Getenv("LYDIABOX_IMAGE_LINK_URL")
	if linkURL == "" {
		t.Skip("LYDIABOX_IMAGE_LINK_URL is not set; deploy/instabox/test.sh sets it")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	l, err := link.New(link.Options{Secret: []byte(os.Getenv("LYDIABOX_IMAGE_LINK_SECRET")), WakeTimeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// The host broker: the guest's /run/lydiabox/host-9001.sock reaches it
	// through the link.
	brokerLn, err := l.Listen(rpc.PortHostBroker)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	envReport := make(chan url.Values, 1)
	broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/env" {
			select {
			case envReport <- r.URL.Query():
			default:
			}
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		select {
		case got <- r.URL.Path:
		default:
		}
		_, _ = io.WriteString(w, "broker ok\n")
	}), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = broker.Serve(brokerLn) }()
	defer broker.Close()

	// Through the InstaBox port proxy the upgrade carries the private
	// port token.
	target := link.Target{URL: linkURL}
	if tok := os.Getenv("LYDIABOX_IMAGE_LINK_TOKEN"); tok != "" {
		target.Header = http.Header{"Authorization": {"Bearer " + tok}}
	}
	if err := l.Connect(target); err != nil {
		t.Fatal(err)
	}
	var sess *rpc.Session
	for sess == nil {
		conn, err := l.Dial(ctx, rpc.PortGuestRPC)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("dial guest RPC: %v (link %+v)", err, l.Status())
			}
			time.Sleep(time.Second)
			continue
		}
		if sess, err = rpc.NewSession(conn); err != nil {
			t.Fatal(err)
		}
	}
	defer sess.Close()

	var ping rpc.PingResult
	if err := sess.Call(ctx, rpc.MethodPing, nil, &ping); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Logf("guest version %q", ping.Version)

	var tools json.RawMessage
	if err := sess.Call(ctx, rpc.MethodToolsList, nil, &tools); err != nil {
		t.Fatalf("mcp.tools (desktop): %v", err)
	}
	if !strings.Contains(string(tools), "computer_screenshot") {
		t.Fatalf("mcp.tools has no screenshot tool: %.200s", tools)
	}
	// A screenshot proves that the daemon reaches the X display.
	var shot json.RawMessage
	err = sess.Call(ctx, rpc.MethodToolCall, rpc.ToolCallParams{Name: "computer_screenshot", Arguments: json.RawMessage(`{}`)}, &shot)
	if err != nil || !strings.Contains(string(shot), "image/png") || strings.Contains(string(shot), `"isError":true`) {
		t.Fatalf("computer_screenshot: %v %.200s", err, shot)
	}

	// A PTY in the workspace calls the broker over its local socket.
	var info rpc.PTYInfo
	err = sess.Call(ctx, rpc.MethodPTYStart, rpc.PTYStartParams{
		Command: []string{"curl", "-s", "--unix-socket", "/run/lydiabox/host-9001.sock", "http://broker/from-guest"},
	}, &info)
	if err != nil {
		t.Fatalf("pty.start: %v", err)
	}
	if info.Dir != "/data/workspace" {
		t.Errorf("PTY dir = %q, want /data/workspace", info.Dir)
	}
	select {
	case p := <-got:
		if p != "/from-guest" {
			t.Fatalf("broker got %q", p)
		}
	case <-ctx.Done():
		t.Fatal("the guest did not reach the host broker through the link")
	}

	checkPTYEnv(ctx, t, sess, envReport)
}
