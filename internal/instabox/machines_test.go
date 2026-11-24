package instabox_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

func TestLifecycleStates(t *testing.T) {
	r := newRig(t, instaboxtest.Options{OpDelay: 50 * time.Millisecond})
	ctx := ctxT(t)
	op, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{
		Name: "life", SnapshotID: "", VCPUs: 2, MemMiB: 4096, Metadata: map[string]string{"tenant": "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Type != instabox.OpCreate || op.State.Done() {
		t.Fatalf("create op = %+v", op)
	}
	m, err := r.client.GetMachine(ctx, "life")
	if err != nil {
		t.Fatal(err)
	}
	if m.State != instabox.StateCreating || !m.State.Transitional() || m.Runtime.Ready {
		t.Fatalf("new machine state = %s ready=%v", m.State, m.Runtime.Ready)
	}
	final := r.done(t)(op, nil)
	if final.Machine == nil || final.Machine.State != instabox.StateRunning || final.Machine.Spec.VCPUs != 2 {
		t.Fatalf("final op machine = %+v", final.Machine)
	}
	id := op.MachineID

	steps := []struct {
		call func() (*instabox.Operation, error)
		want instabox.State
	}{
		{func() (*instabox.Operation, error) { return r.client.PauseMachine(ctx, id) }, instabox.StateSuspended},
		{func() (*instabox.Operation, error) { return r.client.ResumeMachine(ctx, id) }, instabox.StateRunning},
		{func() (*instabox.Operation, error) {
			return r.client.StopMachine(ctx, id, instabox.StopOptions{TimeoutSeconds: 10})
		}, instabox.StateStopped},
		{func() (*instabox.Operation, error) { return r.client.StartMachine(ctx, id, instabox.StartOptions{}) }, instabox.StateRunning},
	}
	for _, st := range steps {
		r.done(t)(st.call())
		m, err := r.client.GetMachine(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if m.State != st.want {
			t.Fatalf("state = %s, want %s", m.State, st.want)
		}
	}

	// start on a running machine is already in effect.
	op, err = r.client.StartMachine(ctx, id, instabox.StartOptions{})
	if err != nil || op.State != instabox.OpSucceeded {
		t.Fatalf("start of running machine: %+v %v", op, err)
	}

	// A different change while one runs is a conflict.
	if _, err := r.client.StopMachine(ctx, id, instabox.StopOptions{}); err != nil {
		t.Fatal(err)
	}
	_, err = r.client.PauseMachine(ctx, id)
	var apiErr *instabox.APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, instabox.ErrConflict) || apiErr.OperationID == nil {
		t.Fatalf("pause during stop: %v", err)
	}
}

func TestStopPausedIsConflict(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "paused")
	r.done(t)(r.client.PauseMachine(ctx, m.ID))
	if _, err := r.client.StopMachine(ctx, m.ID, instabox.StopOptions{}); !errors.Is(err, instabox.ErrConflict) {
		t.Fatalf("stop of paused machine: %v", err)
	}
}

func TestDeleteMachine(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "gone")
	r.done(t)(r.client.DeleteMachine(ctx, m.ID))
	if _, err := r.client.GetMachine(ctx, m.ID); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	reqs := r.fake.Requests()
	for _, req := range reqs {
		if req.Method == http.MethodDelete && req.Header.Get("X-Confirm-Delete") != m.ID {
			t.Fatalf("X-Confirm-Delete = %q", req.Header.Get("X-Confirm-Delete"))
		}
	}
	if _, err := r.client.DeleteMachine(ctx, "m_aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("delete of unknown machine: %v", err)
	}
}

func TestCreateErrors(t *testing.T) {
	r := newRig(t, instaboxtest.Options{MaxMachines: 1})
	ctx := ctxT(t)
	if _, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{TemplateID: "a", SnapshotID: "b"}); err == nil {
		t.Fatal("template and snapshot accepted")
	}
	if _, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{SnapshotID: "sn_missing"}); !errors.Is(err, instabox.ErrNotFound) {
		t.Fatalf("missing snapshot: %v", err)
	}
	if _, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{VCPUs: 3, MemMiB: 1000}); instabox.CodeOf(err) != instabox.CodeSpecNotAllowed {
		t.Fatalf("bad size: %v", err)
	}
	r.running(t, "first")
	_, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "second"})
	if !errors.Is(err, instabox.ErrQuota) || instabox.CodeOf(err) != instabox.CodeQuotaExceeded {
		t.Fatalf("over quota: %v", err)
	}
}

func TestCreateOperationFailsNoCapacity(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	r.fake.FailNextCreate(instabox.CodeNoCapacity, true, 60)
	op, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "full"})
	if err != nil {
		t.Fatal(err)
	}
	final, err := r.client.WaitOperation(ctx, op)
	var apiErr *instabox.APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, instabox.ErrNoCapacity) || apiErr.Operation == nil ||
		final.State != instabox.OpFailed || apiErr.RetryAfter != time.Minute || !apiErr.Retryable {
		t.Fatalf("failed create: %v (%+v)", err, apiErr)
	}

	// failFast refuses at once; the 503 is retryable but its
	// Retry-After is over MaxRetryDelay, so it goes back to the caller.
	r.fake.FailNextCreate(instabox.CodeNoCapacity, true, 60)
	_, err = r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "fast", FailFast: true})
	if !errors.Is(err, instabox.ErrNoCapacity) {
		t.Fatalf("failFast create: %v", err)
	}
}

func TestListMachinesPagesAndFilters(t *testing.T) {
	r := newRig(t, instaboxtest.Options{OpDelay: time.Millisecond})
	ctx := ctxT(t)
	for i, name := range []string{"a1", "a2", "a3", "b1"} {
		tenant := "t1"
		if i == 3 {
			tenant = "t2"
		}
		if _, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: name, Metadata: map[string]string{"tenant": tenant}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := r.client.ListMachines(ctx, instabox.ListMachinesOptions{Limit: 3})
	if err != nil || len(page.Machines) != 3 || page.NextCursor == nil {
		t.Fatalf("first page: %+v %v", page, err)
	}
	all, err := r.client.ListAllMachines(ctx, instabox.ListMachinesOptions{Limit: 3})
	if err != nil || len(all) != 4 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	t1, err := r.client.ListAllMachines(ctx, instabox.ListMachinesOptions{Metadata: map[string]string{"tenant": "t1"}})
	if err != nil || len(t1) != 3 {
		t.Fatalf("tenant t1: %d %v", len(t1), err)
	}
}

func TestIdlePauseAndMaxRun(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "idle")
	got, err := r.client.SetIdlePause(ctx, m.ID, 10*time.Minute)
	if err != nil || got.AutoSuspendAfterSeconds == nil || *got.AutoSuspendAfterSeconds != 600 {
		t.Fatalf("SetIdlePause: %+v %v", got, err)
	}
	got, err = r.client.SetIdlePause(ctx, m.ID, 0)
	if err != nil || got.AutoSuspendAfterSeconds != nil {
		t.Fatalf("SetIdlePause off: %+v %v", got, err)
	}
	if _, err := r.client.SetIdlePause(ctx, m.ID, 90*time.Second); err == nil {
		t.Fatal("part minute accepted")
	}
	if _, err := r.client.SetIdlePause(ctx, m.ID, 2*time.Minute); instabox.CodeOf(err) != instabox.CodeBadRequest {
		t.Fatalf("2 minutes: %v", err)
	}
	got, err = r.client.SetMaxRun(ctx, m.ID, 60)
	if err != nil || got.MaxRunMinutes == nil || *got.MaxRunMinutes != 60 {
		t.Fatalf("SetMaxRun: %+v %v", got, err)
	}
	if _, err := r.client.SetMaxRun(ctx, m.ID, -2); err == nil {
		t.Fatal("-2 accepted")
	}

	free := newRig(t, instaboxtest.Options{FreePlan: true})
	fm := free.running(t, "free")
	if _, err := free.client.SetIdlePause(ctx, fm.ID, 10*time.Minute); !errors.Is(err, instabox.ErrForbidden) {
		t.Fatalf("free plan idle pause: %v", err)
	}
}

func TestWakeOnUse(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "sleepy")
	if !r.fake.IdlePause(m.ID) {
		t.Fatal("IdlePause failed")
	}
	got, err := r.client.GetMachine(ctx, m.ID)
	if err != nil || !got.IdlePaused() {
		t.Fatalf("idle paused machine: %+v %v", got, err)
	}
	// Exec wakes an idle-paused machine.
	res, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Argv: []string{"echo", "hi"}})
	if err != nil || res.Stdout != "hi\n" {
		t.Fatalf("exec on idle machine: %+v %v", res, err)
	}
	r.fake.IdlePause(m.ID)
	woke, err := r.client.WakeMachine(ctx, m.ID)
	if err != nil || woke.State != instabox.StateRunning {
		t.Fatalf("wake: %+v %v", woke, err)
	}
	// A machine paused by a call does not wake on use.
	r.done(t)(r.client.PauseMachine(ctx, m.ID))
	if _, err := r.client.Exec(ctx, m.ID, instabox.ExecRequest{Cmd: "true"}); !errors.Is(err, instabox.ErrNotRunning) {
		t.Fatalf("exec on paused machine: %v", err)
	}
	if _, err := r.client.WakeMachine(ctx, m.ID); !errors.Is(err, instabox.ErrNotRunning) {
		t.Fatalf("wake of paused machine: %v", err)
	}
}

func TestActivityLease(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "lease")
	r.fake.IdlePause(m.ID)
	lease, err := r.client.PutActivityLease(ctx, m.ID, "harness-1", 5*time.Minute)
	if err != nil || lease.ID != "harness-1" || time.Until(lease.ExpiresAt) < 4*time.Minute {
		t.Fatalf("lease: %+v %v", lease, err)
	}
	if r.fake.State(m.ID) != "running" {
		t.Fatal("lease did not wake the machine")
	}
	if err := r.client.DeleteActivityLease(ctx, m.ID, "harness-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.client.DeleteActivityLease(ctx, m.ID, "harness-1"); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if len(r.fake.Leases(m.ID)) != 0 {
		t.Fatal("lease kept")
	}
}

func TestUpdateResizeRestartReturnsOperation(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "resize")
	res, err := r.client.UpdateMachine(ctx, m.ID, instabox.UpdateMachineRequest{VCPUs: 8, MemMiB: 16384, Apply: "restart"})
	if err != nil || res.Operation == nil || res.Operation.Type != instabox.OpResize {
		t.Fatalf("resize: %+v %v", res, err)
	}
	r.done(t)(res.Operation, nil)
}
