package desktop

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/tenant"
)

// blockingGuest does not answer GetMode until the request ends.
type blockingGuest struct {
	*fakeGuest
	calls atomic.Int32
}

func (g *blockingGuest) GetMode(ctx context.Context, _ string) (rpc.ModeState, error) {
	g.calls.Add(1)
	<-ctx.Done()
	return rpc.ModeState{}, ctx.Err()
}

// TestModeWhileTheVMPauses: a VM that pauses does not answer the guest, so
// the mode route answers 503 at once instead of holding the Desktop page
// at "Loading desktop".
func TestModeWhileTheVMPauses(t *testing.T) {
	g := &blockingGuest{fakeGuest: &fakeGuest{mode: rpc.ModeObserver}}
	var paused, held atomic.Bool
	paused.Store(true)
	rl, err := New(Options{Guest: g, Power: &fakePower{running: true}, Logger: slog.New(slog.DiscardHandler),
		Paused: func(string) bool { return paused.Load() }, Stopped: func(string) bool { return held.Load() }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/mode", nil)
	req = req.WithContext(tenant.WithID(req.Context(), "default"))
	rec := httptest.NewRecorder()
	rl.ServeMode(rec, req)
	if rec.Code != http.StatusServiceUnavailable || g.calls.Load() != 0 {
		t.Fatalf("paused: status %d, guest calls %d", rec.Code, g.calls.Load())
	}
	// A stop holds the VM from its start: the same answer while it runs.
	paused.Store(false)
	held.Store(true)
	rec = httptest.NewRecorder()
	rl.ServeMode(rec, req)
	if rec.Code != http.StatusServiceUnavailable || g.calls.Load() != 0 {
		t.Fatalf("stopping: status %d, guest calls %d", rec.Code, g.calls.Load())
	}
}
