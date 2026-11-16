//go:build unix

package instabox_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

func TestCreateIsIdempotentAcrossRestarts(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	ctx := context.Background()
	// The first process loses the answer of the create (a crash at the
	// worst moment): the server made the machine, the client saw nothing.
	lost := &lostAnswer{method: http.MethodPost, path: "/v1/machines", left: 1}
	d1 := e.driver(t, func(o *vmmib.Options) { o.Client = e.clientWith(t, -1, lost) })
	if err := d1.Create(ctx, tenant, vmm.Spec{CPUs: 2, MemMB: 4096}); err == nil {
		t.Fatal("the create did not fail")
	}
	rec, ok, err := vmmib.LoadRecord(ctx, e.store, tenant)
	if err != nil || !ok || rec.CreateKey == "" || rec.MachineID != "" {
		t.Fatalf("the create key was not saved before the call: %+v %v %v", rec, ok, err)
	}
	d1.Close()

	// The next process finds the machine and never makes a second one.
	d2 := e.driver(t)
	for i := 0; i < 2; i++ {
		if err := d2.Create(ctx, tenant, vmm.Spec{CPUs: 2, MemMB: 4096}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.fake.Machines()); n != 1 {
		t.Fatalf("%d machines, want 1", n)
	}
	if n := e.count(http.MethodPost, "/v1/machines"); n != 1 {
		t.Fatalf("%d creates, want 1", n)
	}
	id := e.machineID(t)
	if rec, _, _ := vmmib.LoadRecord(ctx, e.store, tenant); rec.CreateKey != "" || rec.Name != vmmib.MachineName(tenant) {
		t.Fatalf("record %+v", rec)
	}
	// A lost record (a new data directory) adopts the machine by its
	// metadata.
	if err := vmmib.ForgetRecord(ctx, e.store, tenant); err != nil {
		t.Fatal(err)
	}
	d3 := e.driver(t)
	if err := d3.Create(ctx, tenant, vmm.Spec{CPUs: 2, MemMB: 4096}); err != nil {
		t.Fatal(err)
	}
	if got := e.machineID(t); got != id || len(e.fake.Machines()) != 1 {
		t.Fatalf("adopted %s, want %s; %d machines", got, id, len(e.fake.Machines()))
	}
}

func TestCreateReplaysTheSavedKey(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	lost := &lostAnswer{method: http.MethodPost, path: "/v1/machines", left: 1}
	d := e.driver(t, func(o *vmmib.Options) { o.Client = e.clientWith(t, -1, lost) })
	if err := d.Create(ctx, tenant, vmm.Spec{CPUs: 4, MemMB: 8192}); err == nil {
		t.Fatal("the create did not fail")
	}
	// The list of machines does not show the new machine yet (a list that
	// lags), so the next attempt replays the saved create.
	lost2 := &lostAnswer{}
	d2 := e.driver(t, func(o *vmmib.Options) { o.Client = e.clientWith(t, -1, hideList{lost2}) })
	if err := d2.Create(ctx, tenant, vmm.Spec{CPUs: 4, MemMB: 8192}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.fake.Machines()); n != 1 {
		t.Fatalf("%d machines, want 1", n)
	}
	if e.fake.Replays() != 1 {
		t.Fatalf("%d replays, want 1: the second create must send the saved Idempotency-Key", e.fake.Replays())
	}
}

// hideList answers every machine list with an empty page.
type hideList struct{ next http.RoundTripper }

func (h hideList) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && r.URL.Path == "/v1/machines" {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"machines":[],"nextCursor":null}`)), Request: r}, nil
	}
	return h.next.RoundTrip(r)
}

func TestLifecycleThroughTheGuestLink(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	ctx := context.Background()
	d := e.driver(t)
	if st := state(t, d); st != vmm.StateStopped {
		t.Fatalf("state before the create: %s", st)
	}
	boot(t, d)
	id := e.machineID(t)
	if st := state(t, d); st != vmm.StateRunning {
		t.Fatalf("state %s", st)
	}
	ping(t, d)

	// The settings file has the link secret and nothing of the host.
	envFile, mode, ok := e.fake.File(id, "/data/.lydiabox/guest.env")
	if !ok || mode != 0o600 {
		t.Fatalf("guest.env %v mode %o", ok, mode)
	}
	for _, want := range []string{"LYDIABOX_GUEST_LISTEN=ws://0.0.0.0:9000/", "LYDIABOX_LINK_SECRET=", "LYDIABOX_WEBRTC_UDP_PORT=0"} {
		if !strings.Contains(string(envFile), want) {
			t.Errorf("guest.env lacks %q", want)
		}
	}
	// The image owns the file roots (deploy/instabox): the driver sets none.
	if strings.Contains(string(envFile), "_ROOT=") {
		t.Errorf("guest.env sets a file root:\n%s", envFile)
	}
	if strings.Contains(string(envFile), e.fake.APIKey()) || strings.Contains(string(envFile), "ibp_") {
		t.Fatal("guest.env holds the API key or the port token")
	}
	if e.fake.PortToken(id, 9000) == "" {
		t.Fatal("port 9000 is not a private port")
	}
	if leases := e.fake.Leases(id); len(leases) != 1 || leases[0] != vmmib.LeaseID {
		t.Fatalf("leases %v", leases)
	}

	if err := d.Pause(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if st := state(t, d); st != vmm.StatePaused || e.fake.State(id) != "suspended" {
		t.Fatalf("state after pause %s (%s)", st, e.fake.State(id))
	}
	if _, err := d.DialGuest(ctx, tenant, 9000); !errors.Is(err, vmm.ErrUnavailable) {
		t.Fatalf("dial of a paused machine: %v", err)
	}
	if err := d.Resume(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	ping(t, d)
	if e.runner.Boots(id) != 1 {
		t.Fatalf("the resume restarted the guest (%d boots): a pause keeps memory", e.runner.Boots(id))
	}
	// A paused machine is resumed before a stop.
	if err := d.Pause(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := d.Shutdown(ctx, tenant, false); err != nil {
		t.Fatal(err)
	}
	if st := state(t, d); st != vmm.StateStopped || e.fake.State(id) != "stopped" {
		t.Fatalf("state after shutdown %s", st)
	}
	if err := d.Shutdown(ctx, tenant, true); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
	// Boot starts the stopped machine; the guest keeps its settings.
	if err := d.Boot(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	ping(t, d)
	if n := e.count(http.MethodPut, "/v1/machines/"+id+"/files"); n != 1 {
		t.Fatalf("guest.env written %d times, want 1", n)
	}
	if n := e.count(http.MethodDelete, "/v1/machines/"); n != 0 {
		t.Fatalf("the driver deleted something (%d DELETE)", n)
	}
	if err := d.Shutdown(ctx, tenant, true); err != nil {
		t.Fatal(err)
	}
}

func TestOperationConflictIsWaitedFor(t *testing.T) {
	e := newEnv(t)
	// The snapshot can also be named.
	d := e.driver(t, func(o *vmmib.Options) { o.Snapshot = "lydiabox-desktop" })
	boot(t, d)
	id := e.machineID(t)
	ctx := context.Background()
	if err := d.Shutdown(ctx, tenant, false); err != nil {
		t.Fatal(err)
	}
	e.fake.InjectError(http.MethodPost, "/v1/machines/"+id+"/start", http.StatusConflict, ib.CodeOperationConflict, false, 0, 2)
	if err := d.Boot(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if n := e.count(http.MethodPost, "/v1/machines/"+id+"/start"); n != 3 {
		t.Fatalf("%d starts, want 3", n)
	}
}

func TestAccountErrorsAreClearAndStopRetries(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   string
	}{
		{http.StatusUnauthorized, "invalid_key", "refused the API key"},
		{http.StatusPaymentRequired, ib.CodeCreditExhausted, "needs payment"},
		{http.StatusTooManyRequests, ib.CodeQuotaExceeded, "plan limit"},
		{http.StatusServiceUnavailable, ib.CodeNoCapacity, "no capacity"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e := newEnv(t)
			d := e.driver(t)
			boot(t, d)
			id := e.machineID(t)
			ctx := context.Background()
			if err := d.Shutdown(ctx, tenant, false); err != nil {
				t.Fatal(err)
			}
			e.fake.InjectError(http.MethodPost, "/v1/machines/"+id+"/start", tc.status, tc.code, false, 0, 1)
			err := d.Boot(ctx, tenant)
			var acc *vmmib.AccountError
			if !errors.As(err, &acc) || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, vmm.ErrUnavailable) {
				t.Fatalf("boot error %v", err)
			}
			before := len(e.fake.Requests())
			if err := d.Boot(ctx, tenant); !errors.As(err, &acc) {
				t.Fatalf("second boot: %v", err)
			}
			if _, err := d.State(ctx, tenant); !errors.As(err, &acc) {
				t.Fatalf("state: %v", err)
			}
			if n := len(e.fake.Requests()); n != before {
				t.Fatalf("%d API calls after an account error, want none until the block ends", n-before)
			}
		})
	}
}

func TestDeletedMachineGivesAClearStateAndNoNewMachine(t *testing.T) {
	e := newEnv(t)
	d := e.driver(t)
	boot(t, d)
	id := e.machineID(t)
	ctx := context.Background()
	op, err := e.client(t, 0).DeleteMachine(ctx, id)
	if err == nil {
		_, err = e.client(t, 0).WaitOperation(ctx, op)
	}
	if err != nil {
		t.Fatal(err)
	}
	d2 := e.driver(t)
	if _, err := d2.State(ctx, tenant); !errors.Is(err, vmmib.ErrMachineGone) {
		t.Fatalf("state of a deleted machine: %v", err)
	}
	if err := d2.Create(ctx, tenant, vmm.Spec{CPUs: 2, MemMB: 4096}); !errors.Is(err, vmmib.ErrMachineGone) {
		t.Fatalf("create after a delete: %v", err)
	}
	if n := len(e.fake.Machines()); n != 0 {
		t.Fatalf("the driver made a new machine (%d)", n)
	}
	if err := d2.Shutdown(ctx, tenant, true); err != nil {
		t.Fatalf("shutdown of a deleted machine: %v", err)
	}
}

func TestSelfPauseIsReportedAndWokenOnUse(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	d := e.driver(t)
	var ev events
	d.WatchState(ev.add)
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	if !e.fake.IdlePause(id) {
		t.Fatal("idle pause")
	}
	ev.wait(t, vmm.StatePaused)
	if st := state(t, d); st != vmm.StatePaused {
		t.Fatalf("state %s", st)
	}
	// The link does not reconnect by itself: a reconnect would wake it.
	time.Sleep(300 * time.Millisecond)
	if e.fake.State(id) != "suspended" {
		t.Fatalf("the machine woke without a use: %s", e.fake.State(id))
	}
	if err := d.Resume(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	ping(t, d)
	if e.count(http.MethodPost, "/v1/machines/"+id+"/wake") != 1 {
		t.Fatal("an idle-paused machine must be woken, not resumed")
	}
	if e.runner.Boots(id) != 1 {
		t.Fatalf("%d boots", e.runner.Boots(id))
	}
}

func TestChangedGuestSettingsRestartTheGuest(t *testing.T) {
	e := newEnv(t)
	d := e.driver(t)
	boot(t, d)
	ping(t, d)
	id := e.machineID(t)
	ctx := context.Background()
	// A new data directory: the record and the link secret are lost, the
	// machine stays. The next process adopts it with a new secret.
	if err := vmmib.ForgetRecord(ctx, e.store, tenant); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d2 := e.driver(t)
	boot(t, d2)
	ping(t, d2)
	if got := e.machineID(t); got != id {
		t.Fatalf("adopted %s, want %s", got, id)
	}
	if e.runner.Boots(id) != 2 {
		t.Fatalf("%d guest starts, want 2 (a restart for the new secret)", e.runner.Boots(id))
	}
}
