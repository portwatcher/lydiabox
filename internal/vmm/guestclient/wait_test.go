//go:build unix

package guestclient

import (
	"context"
	"testing"
	"time"
)

// TestPTYWaitDoesNotBlockBehindRead: the terminal room keeps a Read
// pending while the program prints nothing. Wait must still honor its
// context (the session DELETE handler waits with a grace period), instead
// of blocking on the read lock until the next output.
func TestPTYWaitDoesNotBlockBehindRead(t *testing.T) {
	c, _, _ := startGuest(t)
	ctx := context.Background()
	p, err := c.StartPTY(ctx, "default", PTYSpec{ID: "quiet", Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Write([]byte("echo ready\n")); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "ready")
	go func() {
		b := make([]byte, 64)
		for {
			if _, err := p.Read(b); err != nil {
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond) // let the Read block on the stream
	wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = p.Wait(wctx)
	if err == nil {
		t.Fatal("Wait returned without an exit")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Wait took %s; it blocked behind Read", d)
	}
	if err := c.KillPTY(ctx, "default", "quiet"); err != nil {
		t.Fatal(err)
	}
}
