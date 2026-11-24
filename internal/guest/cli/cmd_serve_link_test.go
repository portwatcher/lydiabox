//go:build unix

package cli

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	guestlink "github.com/portwatcher/lydiabox/internal/guest/link"
	"github.com/portwatcher/lydiabox/internal/guest/rpc"
	hostlink "github.com/portwatcher/lydiabox/internal/vmm/link"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestServeInLinkModeBridgesBothDirections(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	secret := "serve-link-secret-0123456789"
	t.Setenv(guestlink.EnvSecret, secret)
	addr := freePort(t)
	brokerSock := filepath.Join(dir, "run", "host-9001.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Main(ctx, "lydiabox-guest", []string{"serve", "-listen", "ws://" + addr + "/link", "-no-desktop",
			"-host-addr", "unix://" + brokerSock, "-llm-addr", "", "-connector-mcp-addr", "",
			"-workdir", dir, "-log-level", "error"},
			Env{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr})
	}()

	l, err := hostlink.New(hostlink.Options{Secret: []byte(secret), MinBackoff: 20 * time.Millisecond, WakeTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	broker, err := l.Listen(rpc.PortHostBroker)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("broker")) })}
	go func() { _ = srv.Serve(broker) }()
	defer srv.Close()
	if err := l.Connect(hostlink.Target{URL: "ws://" + addr + "/link"}); err != nil {
		t.Fatal(err)
	}

	// Host to guest: the RPC answers through the link.
	var sess *rpc.Session
	deadline := time.Now().Add(10 * time.Second)
	for {
		dctx, dcancel := context.WithTimeout(ctx, time.Second)
		conn, err := l.Dial(dctx, rpc.PortGuestRPC)
		dcancel()
		if err == nil {
			if sess, err = rpc.NewSession(conn); err == nil {
				var out rpc.PingResult
				if err = sess.Call(ctx, rpc.MethodPing, nil, &out); err == nil {
					break
				}
				sess.Close()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("ping over the link: %v\n%s", err, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer sess.Close()

	// Guest to host: the local broker socket reaches host port 9001.
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", brokerSock)
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get("http://host/")
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 64)
	n, _ := resp.Body.Read(b)
	resp.Body.Close()
	if string(b[:n]) != "broker" {
		t.Fatalf("broker answered %q", b[:n])
	}
	if os.Getenv(guestlink.EnvSecret) != "" {
		t.Fatal("the daemon left the link secret in its environment")
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestServeInLinkModeNeedsASecret(t *testing.T) {
	t.Setenv(guestlink.EnvSecret, "")
	t.Setenv(guestlink.EnvSecretFile, "")
	var stderr bytes.Buffer
	code := Main(context.Background(), "lydiabox-guest", []string{"serve", "-listen", "ws://" + freePort(t), "-no-desktop",
		"-host-addr", "unix:///tmp/lbx-none/host-9001.sock", "-llm-addr", "", "-connector-mcp-addr", ""},
		Env{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr})
	if code == 0 || !strings.Contains(stderr.String(), guestlink.EnvSecret) {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}
