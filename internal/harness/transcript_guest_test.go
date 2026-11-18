package harness_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs/remotefs"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

// TestGuestTranscript reads a transcript in the guest (provider mode) the
// same way as on the host, and wakes a machine that does not run.
func TestGuestTranscript(t *testing.T) {
	e := newProviderEnv(t)
	ctx := context.Background()
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(), Files: e.guest.FS, Discovery: e.disc,
		Store: &harnesstest.Store{}, Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	local, err := adapters.Claude{}.Transcript(e.home, adapterstest.ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	e.running.Store(false)
	got, err := mgr.Transcript(ctx, tenant, harness.ClaudeCode+":"+adapterstest.ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != len(local.Entries) || got.Entries[0].Text != local.Entries[0].Text || !got.Entries[0].Time.Equal(local.Entries[0].Time) {
		t.Fatalf("guest transcript = %+v\nlocal = %+v", got.Entries, local.Entries)
	}
	if e.guest.Waker.Wakes() == 0 || e.guest.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatalf("transcript wakes %d, leases %d", e.guest.Waker.Wakes(), e.guest.Waker.Leases(remotefs.LeaseFiles))
	}
	for _, tc := range []struct {
		id   string
		want error
	}{
		{"claude-code:../../etc", harness.ErrBadRequest},
		{"codex:missing-session", harness.ErrNotFound},
		{"shell:abc", harness.ErrNotFound},
		{"vim:abc", harness.ErrNotFound},
		{"nocolon", harness.ErrNotFound},
	} {
		if _, err := mgr.Transcript(ctx, tenant, tc.id); !errors.Is(err, tc.want) {
			t.Errorf("transcript %s = %v, want %v", tc.id, err, tc.want)
		}
	}
	e.guest.Waker.Set(0, errors.New("boot failed"))
	if _, err := e.disc.Transcript(ctx, tenant, harness.Codex, adapterstest.CodexID); !errors.Is(err, harness.ErrUnavailable) {
		t.Fatalf("transcript while the boot fails = %v", err)
	}
}
