//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
)

func TestServeAnswersRPCAndShutsDown(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "guest.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Main(ctx, "lydiabox-guest", []string{"serve", "-listen", "unix://" + sock, "-no-desktop",
			"-host-addr", "unix://" + filepath.Join(dir, "host-9001.sock"), "-llm-addr", "127.0.0.1:0",
			"-connector-mcp-addr", "127.0.0.1:0", "-workdir", dir, "-log-level", "error"},
			Env{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr})
	}()

	var sess *rpc.Session
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := rpc.Dial(ctx, "unix://"+sock)
		if err == nil {
			sess, err = rpc.NewSession(conn)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not listen: %v (%s)", err, stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer sess.Close()
	var ping rpc.PingResult
	if err := sess.Call(ctx, rpc.MethodPing, nil, &ping); err != nil || ping.Version == "" {
		t.Fatalf("ping %+v %v", ping, err)
	}
	var st rpc.ModeState
	if err := sess.Call(ctx, rpc.MethodModeGet, nil, &st); !rpc.IsCode(err, rpc.CodeUnavailable) {
		t.Fatalf("mode without desktop: %v", err)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestServeRejectsBadFlags(t *testing.T) {
	if code, _, _ := invoke("lydiabox-guest", "serve", "-listen", "tcp://0.0.0.0:1"); code != 1 {
		t.Fatalf("bad listen accepted: %d", code)
	}
}
