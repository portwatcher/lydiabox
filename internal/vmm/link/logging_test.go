package link

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// logRecorder keeps the level and the text of each log record.
type logRecorder struct {
	mu      sync.Mutex
	records []string
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	line := rec.Level.String() + " " + rec.Message
	rec.Attrs(func(a slog.Attr) bool {
		line += " " + a.Key + "=" + a.Value.String()
		return true
	})
	r.mu.Lock()
	r.records = append(r.records, line)
	r.mu.Unlock()
	return nil
}
func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

// lines returns the records that start with level and contain msg.
func (r *logRecorder) lines(level, msg string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, l := range r.records {
		if strings.HasPrefix(l, level+" ") && strings.Contains(l, msg) {
			out = append(out, l)
		}
	}
	return out
}

// While the machine boots, the provider proxy answers HTTP 502: these
// failed attempts are expected and are not warnings.
func TestBootTimeLinkFailuresAreInfo(t *testing.T) {
	checkLeaks(t)
	rec := &logRecorder{}
	r := newRig(t, rigOptions{badGateway: 3, logger: slog.New(rec)})
	waitState(t, r.link, StateUp)
	if w := rec.lines("WARN", ""); len(w) != 0 {
		t.Fatalf("warnings during a boot: %q", w)
	}
	info := rec.lines("INFO", "guest link attempt failed")
	if len(info) != 3 || !strings.Contains(info[0], "502") {
		t.Fatalf("info lines %q", info)
	}
}

// A link that does not come up within the boot time limit logs one
// warning with the last cause.
func TestLinkDownAfterBootTimeoutWarnsOnce(t *testing.T) {
	checkLeaks(t)
	rec := &logRecorder{}
	r := newRig(t, rigOptions{badGateway: 1 << 30, bootTimeout: 300 * time.Millisecond, logger: slog.New(rec)})
	deadline := time.Now().Add(10 * time.Second)
	for len(rec.lines("INFO", "guest link attempt failed")) < 6 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	r.link.Suspend()
	w := rec.lines("WARN", "")
	if len(w) != 1 || !strings.Contains(w[0], "boot time limit") || !strings.Contains(w[0], "502") {
		t.Fatalf("warnings %q", w)
	}
	if len(rec.lines("INFO", "guest link attempt failed")) < 2 {
		t.Fatal("no failed attempts before the limit")
	}
}
