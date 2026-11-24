package harness_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/portwatcher/lydiabox/internal/harness"
)

// pausingGuest fails every call; before it fails it runs during (for
// example a pause that starts while the call runs).
type pausingGuest struct{ during func() }

func (g pausingGuest) FSCall(context.Context, string, string, any, any) error {
	if g.during != nil {
		g.during()
	}
	return errors.New("use of closed network connection")
}

func discoverLog(t *testing.T, running *atomic.Bool, during func()) string {
	t.Helper()
	var buf bytes.Buffer
	g := &harness.GuestDiscovery{Guest: pausingGuest{during: during},
		Running: func(string) bool { return running.Load() },
		Logger:  slog.New(slog.NewTextHandler(&buf, nil))}
	out, err := g.Discover(context.Background(), "default", []string{"claude"})
	if err != nil || !out.Cached {
		t.Fatalf("discover: %+v %v", out, err)
	}
	return buf.String()
}

// A discovery that fails because the machine paused or stopped during the
// call is expected (info); one that fails while it runs is a warning.
func TestGuestDiscoveryFailureLevel(t *testing.T) {
	var running atomic.Bool
	running.Store(true)
	if out := discoverLog(t, &running, func() { running.Store(false) }); !strings.Contains(out, "level=INFO") || strings.Contains(out, "level=WARN") {
		t.Fatalf("failure during a pause:\n%s", out)
	}
	running.Store(true)
	if out := discoverLog(t, &running, nil); !strings.Contains(out, "level=WARN") {
		t.Fatalf("failure while running:\n%s", out)
	}
	// A paused machine is not asked at all.
	running.Store(false)
	asked := false
	if out := discoverLog(t, &running, func() { asked = true }); asked || out != "" {
		t.Fatalf("paused machine asked %v, log:\n%s", asked, out)
	}
}
