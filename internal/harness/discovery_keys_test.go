//go:build unix

package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/harness"
)

// extraKeysGuest answers every discovery with a new harness name that the
// host did not ask for.
type extraKeysGuest struct{ n int }

func (g *extraKeysGuest) FSCall(_ context.Context, _, method string, _, out any) error {
	switch method {
	case rpc.MethodFSRoots:
		return errors.New("unknown method")
	case rpc.MethodHarnessDiscover:
		g.n++
		*out.(*rpc.HarnessDiscoverResult) = rpc.HarnessDiscoverResult{Sessions: map[string]json.RawMessage{
			harness.ClaudeCode:           json.RawMessage(`[]`),
			fmt.Sprintf("extra-%d", g.n): json.RawMessage(`[{"nativeId":"x"}]`),
		}}
		return nil
	}
	return errors.New("unexpected method " + method)
}

// The guest chooses the keys of a discovery answer: only the harnesses
// that the host asked for reach the cache.
func TestGuestDiscoveryCachesOnlyRequestedHarnesses(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	disc := &harness.GuestDiscovery{Guest: &extraKeysGuest{}, Cache: st, Running: func(string) bool { return true }, Logger: quiet()}
	for i := 0; i < 3; i++ {
		if _, err := disc.Discover(ctx, "default", []string{harness.ClaudeCode}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := st.ListDiscoveryCache(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Harness != harness.ClaudeCode {
			t.Fatalf("cache holds unrequested harness %q", e.Harness)
		}
	}
}
