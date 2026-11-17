package instabox

import (
	"errors"
	"strings"
	"testing"
	"time"

	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

func TestLinkURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://9000-abc.instabox.dev":       "wss://9000-abc.instabox.dev/",
		"https://9000-abc.instabox.dev/?x=1":  "wss://9000-abc.instabox.dev/",
		"http://127.0.0.1:5000/p/9000-abc":    "ws://127.0.0.1:5000/p/9000-abc/",
		"http://127.0.0.1:5000/p/9000-abc/#f": "ws://127.0.0.1:5000/p/9000-abc/",
	} {
		if got, err := LinkURL(in); err != nil || got != want {
			t.Errorf("LinkURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://"} {
		if _, err := LinkURL(bad); err == nil {
			t.Errorf("LinkURL(%q) was accepted", bad)
		}
	}
}

func TestMachineNameAndStates(t *testing.T) {
	if a, b := MachineName("alice"), MachineName("bob"); a == b || !strings.HasPrefix(a, "lbx-") || len(a) != 20 {
		t.Fatalf("names %q %q", a, b)
	}
	for st, want := range map[ib.State]vmm.State{
		ib.StateRunning: vmm.StateRunning, ib.StateCreating: vmm.StateBooting, ib.StateStarting: vmm.StateBooting,
		ib.StateResuming: vmm.StateBooting, ib.StateSuspending: vmm.StatePaused, ib.StateSuspended: vmm.StatePaused,
		ib.StateStopping: vmm.StateStopped, ib.StateStopped: vmm.StateStopped, ib.StateError: vmm.StateStopped,
		ib.StateDeleted: vmm.StateStopped,
	} {
		if got := mapState(&ib.Machine{State: st}); got != want {
			t.Errorf("%s maps to %s, want %s", st, got, want)
		}
	}
}

func TestGuestEnvHasOnlyGuestSettings(t *testing.T) {
	env := string(guestEnv("s3cret-link-secret-0123", 9000))
	want := "LYDIABOX_GUEST_LISTEN=ws://0.0.0.0:9000/\nLYDIABOX_LINK_SECRET=s3cret-link-secret-0123\n" +
		"LYDIABOX_WEBRTC_UDP_PORT=0\n"
	if env != want {
		t.Fatalf("guest.env:\n%s", env)
	}
}

func TestClassifyAndGate(t *testing.T) {
	now := time.Unix(1000, 0)
	plain := errors.New("boom")
	if got := classify(plain, now, time.Minute); got != plain {
		t.Fatalf("a plain error changed: %v", got)
	}
	err := classify(&ib.APIError{Status: 503, ErrorDetail: ib.ErrorDetail{Code: ib.CodeNoCapacity, Message: "full"}, RetryAfter: 2 * time.Minute},
		now, time.Minute)
	var acc *AccountError
	if !errors.As(err, &acc) || !acc.After.Equal(now.Add(2*time.Minute)) || !errors.Is(err, vmm.ErrUnavailable) || !errors.Is(err, ib.ErrNoCapacity) {
		t.Fatalf("no capacity: %v", err)
	}
	var g gate
	g.record("t", err)
	if g.check("t", now.Add(time.Minute)) == nil || g.check("u", now) != nil {
		t.Fatal("gate")
	}
	if g.check("t", now.Add(3*time.Minute)) != nil {
		t.Fatal("the gate did not open after the wait")
	}
	g.record("t", err)
	g.record("t", nil)
	if g.check("t", now) != nil {
		t.Fatal("a success did not open the gate")
	}
	if err := classify(&ib.APIError{Status: 404, ErrorDetail: ib.ErrorDetail{Code: ib.CodeNotFound}}, now, time.Minute); !errors.Is(err, ErrMachineGone) {
		t.Fatalf("not found: %v", err)
	}
	// The cooldown after a pause at the maximum run time blocks wakes until
	// it ends, with a reason that says so.
	cool := classify(&ib.APIError{Status: 409, ErrorDetail: ib.ErrorDetail{Code: ib.CodeMachineCooldown, Message: "cooling down"},
		RetryAfter: 16 * time.Minute}, now, time.Minute)
	if !errors.As(cool, &acc) || !acc.After.Equal(now.Add(16*time.Minute)) || !errors.Is(cool, vmm.ErrUnavailable) ||
		!strings.Contains(acc.Reason, "maximum run time") || !strings.Contains(acc.Reason, now.Add(16*time.Minute).UTC().Format("15:04 UTC")) {
		t.Fatalf("cooldown: %v", cool)
	}
}

func TestPauseReason(t *testing.T) {
	now := time.Date(2026, 11, 17, 13, 50, 30, 0, time.UTC)
	later, earlier := now.Add(16*time.Minute), now.Add(-time.Hour)
	maxRun := 120
	cases := []struct {
		name string
		m    ib.Machine
		want string
	}{
		{"max run", ib.Machine{State: ib.StateSuspended, AutoSuspendedAt: &now, CooldownUntil: &later, MaxRunMinutes: &maxRun}, "maximum run time"},
		{"idle", ib.Machine{State: ib.StateSuspended, AutoSuspendedAt: &now, CooldownUntil: &earlier}, "idle pause"},
		{"call", ib.Machine{State: ib.StateSuspended}, ""},
		{"stopped", ib.Machine{State: ib.StateStopped, CooldownUntil: &later}, ""},
	}
	for _, c := range cases {
		if got := pauseReason(&c.m, now); got != c.want {
			t.Errorf("%s: pauseReason = %q, want %q", c.name, got, c.want)
		}
	}
}
