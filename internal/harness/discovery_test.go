//go:build unix

package harness_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs/agentfstest"
	"github.com/lydiabox/lydiabox/internal/agentfs/remotefs"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
	"github.com/lydiabox/lydiabox/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ids(d harness.Discovered) []string {
	var out []string
	for h, metas := range d.Sessions {
		for _, m := range metas {
			out = append(out, h+":"+m.NativeID+":"+m.Title+":"+m.WorkspaceDir+":"+m.Model)
		}
	}
	sort.Strings(out)
	return out
}

type providerEnv struct {
	home    string
	guest   *agentfstest.Guest
	running atomic.Bool
	disc    *harness.GuestDiscovery
	store   *store.Store
}

func newProviderEnv(t *testing.T) *providerEnv {
	home := adapterstest.Home(t)
	ws := filepath.Join(filepath.Dir(filepath.Dir(home)), "workspace")
	e := &providerEnv{home: home, guest: agentfstest.Start(t, home, ws), store: openStore(t)}
	e.running.Store(true)
	e.disc = &harness.GuestDiscovery{Guest: e.guest.Client, Cache: e.store, Begin: e.guest.FS.Begin,
		Running: func(string) bool { return e.running.Load() }, Logger: quiet()}
	return e
}

// TestGuestDiscoveryMatchesLocal runs the adapters on the host (juicefs
// mode) and in the guest daemon (provider mode) over the same home tree.
func TestGuestDiscoveryMatchesLocal(t *testing.T) {
	e := newProviderEnv(t)
	ctx := context.Background()
	local, err := harness.LocalDiscovery{Dirs: harnesstest.Dirs{Home: e.home}, Adapters: adapters.All()}.Discover(ctx, tenant, nil)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := e.disc.Discover(ctx, tenant, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, got := ids(local), ids(guest)
	if len(want) < 5 || len(guest.Errors) != 0 || guest.Cached {
		t.Fatalf("guest discovery = %v (errors %v, cached %v)", got, guest.Errors, guest.Cached)
	}
	if len(want) != len(got) {
		t.Fatalf("local %v\nguest %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("local %v\nguest %v", want, got)
		}
	}
	if e.guest.Waker.Wakes() != 0 {
		t.Fatal("discovery woke the machine")
	}
}

func TestGuestDiscoveryCacheWhileStopped(t *testing.T) {
	e := newProviderEnv(t)
	ctx := context.Background()
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: e.guest.FS, Discovery: e.disc,
		Store: &harnesstest.Store{}, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := mgr.ListSessions(ctx, tenant, harness.Filter{})
	if err != nil || fresh.Cached || fresh.CachedAt != nil || len(fresh.Sessions) < 5 {
		t.Fatalf("fresh list = %+v, %v", fresh, err)
	}
	// The machine stops: the list comes from the state database and does
	// not wake it.
	e.running.Store(false)
	cached, err := mgr.ListSessions(ctx, tenant, harness.Filter{})
	if err != nil || !cached.Cached || cached.CachedAt == nil || len(cached.Sessions) != len(fresh.Sessions) {
		t.Fatalf("cached list = %+v, %v", cached, err)
	}
	if e.guest.Waker.Wakes() != 0 {
		t.Fatal("listing a stopped machine woke it")
	}
	one, err := mgr.ListSessions(ctx, tenant, harness.Filter{Harness: harness.ClaudeCode})
	if err != nil || len(one.Sessions) == 0 {
		t.Fatalf("cached filter = %+v, %v", one, err)
	}
	for _, s := range one.Sessions {
		if s.Harness != harness.ClaudeCode {
			t.Fatalf("filter returned %s", s.Harness)
		}
	}
	// A guest that does not answer while marked running falls back to the
	// cache too.
	e.running.Store(true)
	e.disc.Guest = failingGuest{}
	if l, err := mgr.ListSessions(ctx, tenant, harness.Filter{}); err != nil || !l.Cached {
		t.Fatalf("list with a failing guest = %+v, %v", l, err)
	}
}

type failingGuest struct{}

func (failingGuest) FSCall(context.Context, string, string, any, any) error {
	return errors.New("connection refused")
}

// TestGuestPurge deletes a transcript in the guest; it wakes the machine
// under a files lease and drops the session from the cache.
func TestGuestPurge(t *testing.T) {
	e := newProviderEnv(t)
	ctx := context.Background()
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: e.guest.FS, Discovery: e.disc,
		Store: &harnesstest.Store{}, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.ListSessions(ctx, tenant, harness.Filter{}); err != nil {
		t.Fatal(err)
	}
	id := "dsh:" + adapterstest.DSHID
	if err := mgr.Delete(ctx, tenant, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.home, ".dsh/sessions/---workspace-lydiabox--", adapterstest.DSHID)); !os.IsNotExist(err) {
		t.Fatalf("transcript not purged: %v", err)
	}
	if e.guest.Waker.Wakes() == 0 || e.guest.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatalf("purge wakes %d, leases %d", e.guest.Waker.Wakes(), e.guest.Waker.Leases(remotefs.LeaseFiles))
	}
	e.running.Store(false)
	l, err := mgr.ListSessions(ctx, tenant, harness.Filter{Harness: harness.DSH})
	if err != nil || !l.Cached {
		t.Fatalf("cached list = %+v, %v", l, err)
	}
	for _, s := range l.Sessions {
		if s.ID == id {
			t.Fatal("the purged session is still cached")
		}
	}
	// An invalid ID is refused by the adapter in the guest.
	if err := e.disc.Purge(ctx, tenant, harness.ClaudeCode, "../../etc"); !errors.Is(err, harness.ErrBadRequest) {
		t.Fatalf("purge with a bad ID = %v", err)
	}
	// A purge while the machine does not start answers unavailable.
	e.guest.Waker.Set(0, errors.New("boot failed"))
	if err := e.disc.Purge(ctx, tenant, harness.Codex, adapterstest.CodexID); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("purge while the boot fails = %v", err)
	}
}

// TestWorkspaceCreatedInGuest checks that a session start creates its
// workspace directory through the guest in the provider mode.
func TestWorkspaceCreatedInGuest(t *testing.T) {
	e := newProviderEnv(t)
	ctx := context.Background()
	runner := &harnesstest.Runner{}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: e.guest.FS, Discovery: e.disc,
		Store: &harnesstest.Store{}, Runner: runner, Waker: &harnesstest.Waker{}, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(ctx, tenant, harness.CreateRequest{Harness: harness.Shell, WorkspaceDir: "/workspace/new/proj"}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(e.guest.Workspace, "new", "proj")); err != nil || !fi.IsDir() {
		t.Fatalf("workspace not created in the guest: %v", err)
	}
}
