//go:build unix

package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/harness"
)

// linkedRootsGuest answers like the InstaBox image: /workspace links to
// /data/workspace and /home/agent to /data/lydiabox/home, and Claude Code
// records the link target as its working directory.
type linkedRootsGuest struct {
	dirs      []string
	rootCalls atomic.Int32
	noRoots   bool
}

func (g *linkedRootsGuest) FSCall(_ context.Context, _, method string, _, out any) error {
	switch method {
	case rpc.MethodFSRoots:
		g.rootCalls.Add(1)
		if g.noRoots {
			return errors.New("unknown method")
		}
		*out.(*rpc.FSRootsResult) = rpc.FSRootsResult{Roots: []rpc.FSRoot{
			{Name: "home", GuestPath: "/data/lydiabox/home"}, {Name: "workspace", GuestPath: "/data/workspace/"}}}
		return nil
	case rpc.MethodHarnessDiscover:
		var metas []harness.SessionMeta
		for i, d := range g.dirs {
			metas = append(metas, harness.SessionMeta{Harness: harness.ClaudeCode, NativeID: string(rune('a' + i)), WorkspaceDir: d})
		}
		raw, _ := json.Marshal(metas)
		*out.(*rpc.HarnessDiscoverResult) = rpc.HarnessDiscoverResult{Sessions: map[string]json.RawMessage{harness.ClaudeCode: raw}}
		return nil
	}
	return errors.New("unexpected method " + method)
}

func dirsOf(d harness.Discovered) []string {
	var out []string
	for _, m := range d.Sessions[harness.ClaudeCode] {
		out = append(out, m.WorkspaceDir)
	}
	return out
}

// TestGuestDiscoveryUsesTheLydiaBoxPathForm: the New session dialog fills
// in the folder of the selected session, and only /workspace and
// /home/agent paths are accepted, so a discovered link target is written
// in that form, also in the cache that a paused VM lists.
func TestGuestDiscoveryUsesTheLydiaBoxPathForm(t *testing.T) {
	ctx := context.Background()
	guest := &linkedRootsGuest{dirs: []string{"/data/workspace", "/data/workspace/app", "/data/lydiabox/home/notes",
		"/workspace/kept", "/data/workspace-other", "/srv/else", ""}}
	var running atomic.Bool
	running.Store(true)
	disc := &harness.GuestDiscovery{Guest: guest, Cache: openStore(t), Running: func(string) bool { return running.Load() }, Logger: quiet()}
	want := []string{"/workspace", "/workspace/app", "/home/agent/notes", "/workspace/kept", "/data/workspace-other", "/srv/else", ""}
	for range 2 {
		got, err := disc.Discover(ctx, tenant, nil)
		if err != nil || got.Cached {
			t.Fatalf("discover = %+v, %v", got, err)
		}
		if g := dirsOf(got); !equal(g, want) {
			t.Fatalf("dirs = %q, want %q", g, want)
		}
	}
	if n := guest.rootCalls.Load(); n != 1 {
		t.Fatalf("fs.roots calls = %d, want 1 (kept for the process)", n)
	}
	running.Store(false)
	cached, err := disc.Discover(ctx, tenant, nil)
	if err != nil || !cached.Cached {
		t.Fatalf("cached = %+v, %v", cached, err)
	}
	if g := dirsOf(cached); !equal(g, want) {
		t.Fatalf("cached dirs = %q, want %q", g, want)
	}
}

// TestGuestDiscoveryWithoutRoots keeps the paths when the guest does not
// answer fs.roots, and asks again at the next discovery.
func TestGuestDiscoveryWithoutRoots(t *testing.T) {
	guest := &linkedRootsGuest{dirs: []string{"/data/workspace"}, noRoots: true}
	disc := &harness.GuestDiscovery{Guest: guest, Logger: quiet()}
	for range 2 {
		got, err := disc.Discover(context.Background(), tenant, nil)
		if err != nil || !equal(dirsOf(got), []string{"/data/workspace"}) {
			t.Fatalf("discover = %+v, %v", got, err)
		}
	}
	if n := guest.rootCalls.Load(); n != 2 {
		t.Fatalf("fs.roots calls = %d, want 2", n)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
