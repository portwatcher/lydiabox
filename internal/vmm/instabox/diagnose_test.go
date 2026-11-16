//go:build unix

package instabox_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest"
	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest/fakeguest"
	"github.com/lydiabox/lydiabox/internal/vmm"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

// silentGuest is a machine whose guest daemon never listens: the port
// proxy answers 502, as on the first real tenant (2026-11-16), where the
// image's supervisord could not start the programs.
type silentGuest struct{ *fakeguest.Runner }

func (silentGuest) Addr(string, int) string { return "" }

// logBuffer is a log destination that is safe for concurrent writes.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestBadGatewaySaysTheGuestDaemonDoesNotAnswer(t *testing.T) {
	checkLeaks(t)
	e := newEnvWith(t, func(r *fakeguest.Runner) instaboxtest.GuestRunner { return silentGuest{r} })
	logs := &logBuffer{}
	d := e.driver(t, func(o *vmmib.Options) {
		o.GuestStatusAfter = 100 * time.Millisecond
		o.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	boot(t, d)

	deadline := time.Now().Add(10 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := d.DialGuest(ctx, tenant, 1)
		cancel()
		if err == nil {
			t.Fatal("dial succeeded without a guest daemon")
		}
		if strings.Contains(err.Error(), "machine runs, but its guest daemon does not answer on port 9000") {
			if !errors.Is(err, vmm.ErrUnavailable) {
				t.Fatalf("error %v does not wrap vmm.ErrUnavailable", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial error %v, want the 502 explanation", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	for !strings.Contains(logs.String(), "machine status") {
		if time.Now().After(deadline) {
			t.Fatalf("no status line in the log:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.mu.Lock()
	var status int
	for _, c := range e.cmds {
		if strings.Contains(c, "systemctl is-active instabox-image.service") {
			status++
		}
	}
	e.mu.Unlock()
	if status != 1 {
		t.Fatalf("status command ran %d times, want 1", status)
	}
	if strings.Contains(logs.String(), "LYDIABOX_LINK_SECRET") {
		t.Fatal("the log has the link secret")
	}
}
