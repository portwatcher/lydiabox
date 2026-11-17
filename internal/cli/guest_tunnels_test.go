package cli

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// acceptUntilClosed stands for a server that does not watch its context:
// it ends only when its listener is closed.
func acceptUntilClosed(_ context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		c.Close()
	}
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// At the host shutdown the driver closes the listeners: a tunnel that ends
// then is expected (info), not an error.
func TestGuestTunnelEndAtHostShutdownIsInfo(t *testing.T) {
	buf := &lockedBuffer{}
	tun := newGuestTunnels(slog.New(slog.NewTextHandler(buf, nil)))
	ln := listen(t)
	tun.serve("guest LLM gateway tunnel", ln, acceptUntilClosed)
	tun.cancel()
	ln.Close() // the driver's Close
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tun.stop(ctx)
	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "guest LLM gateway tunnel stopped with the host") {
		t.Fatalf("want one info line, got:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") {
		t.Fatalf("host shutdown logged an error:\n%s", out)
	}
}

// A tunnel that ends while the host runs is an error.
func TestGuestTunnelEndWhileRunningIsError(t *testing.T) {
	buf := &lockedBuffer{}
	tun := newGuestTunnels(slog.New(slog.NewTextHandler(buf, nil)))
	ln := listen(t)
	tun.serve("guest LLM gateway tunnel", ln, acceptUntilClosed)
	ln.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), "level=ERROR") {
		if time.Now().After(deadline) {
			t.Fatalf("no error line:\n%s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(buf.String(), `msg="guest LLM gateway tunnel stopped"`) {
		t.Fatalf("error line lacks the tunnel name:\n%s", buf.String())
	}
	tun.stop(context.Background())
}

// stop waits for a server that watches its context, so the driver closes
// the listener only after the server has ended.
func TestGuestTunnelStopWaitsForTheServer(t *testing.T) {
	tun := newGuestTunnels(slog.New(slog.NewTextHandler(&lockedBuffer{}, nil)))
	ln := listen(t)
	ended := make(chan struct{})
	tun.serve("guest connector broker", ln, func(ctx context.Context, ln net.Listener) error {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond)
		close(ended)
		return ln.Close()
	})
	tun.stop(context.Background())
	select {
	case <-ended:
	default:
		t.Fatal("stop returned before the server ended")
	}
}
