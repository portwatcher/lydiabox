package instabox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/vmm"
)

// update changes the record of a tenant under the record lock.
func (d *Driver) update(ctx context.Context, tid string, fn func(*Record, *recordSecrets)) (Record, recordSecrets, error) {
	d.recMu.Lock()
	defer d.recMu.Unlock()
	rec, sec, ok, err := loadRecord(ctx, d.opts.Store, tid)
	if err != nil {
		return rec, sec, err
	}
	before, beforeSec := rec, sec
	if rec.Name == "" {
		rec.Name = MachineName(tid)
	}
	if sec.LinkSecret == "" {
		sec.LinkSecret = newLinkSecret()
	}
	fn(&rec, &sec)
	if ok && rec == before && sec == beforeSec {
		return rec, sec, nil
	}
	return rec, sec, saveRecord(ctx, d.opts.Store, tid, rec, sec)
}

// Create implements vmm.Driver: it finds the machine of the tenant or
// makes it from the snapshot. The create is idempotent across restarts:
// the Idempotency-Key is saved before the call, and a machine with the
// tenant's metadata is adopted instead of making a second one.
func (d *Driver) Create(ctx context.Context, tid string, spec vmm.Spec) error {
	if err := vmm.ValidTenantID(tid); err != nil {
		return err
	}
	if err := d.gate.check(tid, d.opts.Now()); err != nil {
		return err
	}
	t := d.tenant(tid)
	t.op.Lock()
	defer t.op.Unlock()
	rec, _, err := d.update(ctx, tid, func(*Record, *recordSecrets) {})
	if err != nil {
		return err
	}
	if rec.MachineID != "" {
		m, err := d.opts.Client.GetMachine(ctx, rec.MachineID)
		if err != nil {
			return d.fail(tid, err)
		}
		if m.State == ib.StateDeleted || m.State == ib.StateDeleting {
			return fmt.Errorf("%w: %w", vmm.ErrUnavailable, ErrMachineGone)
		}
		return nil
	}
	found, err := d.findMachine(ctx, tid, rec.Name)
	if err != nil {
		return d.fail(tid, err)
	}
	if found != nil {
		d.opts.Logger.Info("instabox: adopted the machine of the tenant", "tenant", tid, "machine", found.ID)
		_, _, err := d.update(ctx, tid, func(r *Record, _ *recordSecrets) {
			r.MachineID, r.CreateKey, r.EnvSHA256, r.GuestSHA256 = found.ID, "", "", ""
		})
		return err
	}
	if rec.CreateKey == "" {
		if rec, _, err = d.update(ctx, tid, func(r *Record, _ *recordSecrets) { r.CreateKey = ib.NewIdempotencyKey() }); err != nil {
			return err
		}
	}
	snap, err := d.snapshotID(ctx)
	if err != nil {
		return err
	}
	req := ib.CreateMachineRequest{
		Name: rec.Name, SnapshotID: snap, VCPUs: spec.CPUs, MemMiB: spec.MemMB,
		DesiredState: ib.StateRunning, Metadata: map[string]string{MetadataTenant: tid},
	}
	idle := int(d.opts.IdlePause / time.Second)
	req.AutoSuspendAfterSeconds = &idle
	op, err := d.opts.Client.CreateMachine(ib.WithIdempotencyKey(ctx, rec.CreateKey), req)
	if err != nil {
		if errors.Is(err, ib.ErrNotFound) {
			return fmt.Errorf("%w: the InstaBox snapshot %q does not exist (LYDIABOX_INSTABOX_SNAPSHOT)", vmm.ErrUnavailable, d.opts.Snapshot)
		}
		return d.fail(tid, err)
	}
	if _, _, err := d.update(ctx, tid, func(r *Record, _ *recordSecrets) { r.MachineID, r.CreateKey = op.MachineID, "" }); err != nil {
		return err
	}
	d.opts.Logger.Info("instabox: made the machine of the tenant", "tenant", tid, "machine", op.MachineID)
	if _, err := d.opts.Client.WaitOperation(ctx, op); err != nil {
		// The machine exists in the error state; the boot starts it again.
		return d.fail(tid, err)
	}
	d.gate.record(tid, nil)
	return nil
}

// snapshotID returns the configured snapshot ID; a name (not "sn_...") is
// looked up.
func (d *Driver) snapshotID(ctx context.Context) (string, error) {
	if strings.HasPrefix(d.opts.Snapshot, "sn_") {
		return d.opts.Snapshot, nil
	}
	sn, err := d.opts.Client.FindSnapshot(ctx, d.opts.Snapshot)
	if errors.Is(err, ib.ErrNotFound) {
		return "", fmt.Errorf("%w: the InstaBox snapshot %q does not exist (LYDIABOX_INSTABOX_SNAPSHOT)", vmm.ErrUnavailable, d.opts.Snapshot)
	}
	if err != nil {
		return "", err
	}
	return sn.ID, nil
}

// findMachine returns the live machine with the tenant's metadata, or nil.
func (d *Driver) findMachine(ctx context.Context, tid, name string) (*ib.Machine, error) {
	list, err := d.opts.Client.ListAllMachines(ctx, ib.ListMachinesOptions{Metadata: map[string]string{MetadataTenant: tid}})
	if err != nil {
		return nil, err
	}
	var live []ib.Machine
	for _, m := range list {
		if m.State != ib.StateDeleted && m.State != ib.StateDeleting && m.Metadata[MetadataTenant] == tid {
			live = append(live, m)
		}
	}
	switch len(live) {
	case 0:
		return nil, nil
	case 1:
		return &live[0], nil
	}
	for i := range live {
		if live[i].Name == name {
			return &live[i], nil
		}
	}
	return nil, fmt.Errorf("instabox: %d machines have the metadata %s=%s and none is named %s; delete the extra ones",
		len(live), MetadataTenant, tid, name)
}

// Boot implements vmm.Driver: it starts the machine (or resumes it) and
// attaches the guest link.
func (d *Driver) Boot(ctx context.Context, tid string) error {
	if err := d.gate.check(tid, d.opts.Now()); err != nil {
		return err
	}
	t := d.tenant(tid)
	t.op.Lock()
	defer t.op.Unlock()
	id, err := d.machineID(ctx, tid)
	if err != nil {
		return err
	}
	m, err := d.bringUp(ctx, tid, id)
	if err != nil {
		return err
	}
	return d.attach(ctx, tid, m)
}

// Resume implements vmm.Driver. An idle-paused machine is woken; a machine
// that this driver paused is resumed.
func (d *Driver) Resume(ctx context.Context, tid string) error {
	return d.Boot(ctx, tid)
}

// bringUp moves the machine to running: start a stopped one, resume a
// paused one, and wait for a transition. Callers hold t.op. It logs what
// it asks InstaBox for (info), how long the machine took to run, and,
// when ctx ends first, the action, the operation, and the time it waited
// (warning), so that a slow start or resume shows in the log.
func (d *Driver) bringUp(ctx context.Context, tid, id string) (m *ib.Machine, err error) {
	start := time.Now()
	action, opID, waited := "", "", false
	defer func() {
		if err != nil && ctx.Err() != nil {
			d.opts.Logger.Warn("instabox: the machine did not run before the bound ended; InstaBox can still bring it up",
				"tenant", tid, "machine", id, "action", action, "operation", opID,
				"waited", time.Since(start).Round(time.Millisecond), "err", err)
		}
	}()
	for {
		m, err := d.opts.Client.GetMachine(ctx, id)
		if err != nil {
			return nil, d.fail(tid, err)
		}
		var op *ib.Operation
		switch m.State {
		case ib.StateRunning:
			d.gate.record(tid, nil)
			if action != "" {
				d.logUp(tid, m, action, time.Since(start))
			}
			return m, nil
		case ib.StateDeleted, ib.StateDeleting:
			return nil, fmt.Errorf("%w: %w", vmm.ErrUnavailable, ErrMachineGone)
		case ib.StateSuspended:
			if m.IdlePaused() {
				if action != "wake" {
					d.opts.Logger.Info("instabox: waking the idle-paused machine", "tenant", tid, "machine", id)
				}
				action = "wake"
				if _, err := d.opts.Client.WakeMachine(ctx, id); err != nil && !errors.Is(err, ib.ErrConflict) {
					return nil, d.fail(tid, err)
				}
				continue
			}
			action = "resume"
			op, err = d.opts.Client.ResumeMachine(ctx, id)
		case ib.StateStopped, ib.StateError:
			action = "start"
			op, err = d.opts.Client.StartMachine(ctx, id, ib.StartOptions{})
		default:
			// A transition runs: wait for it.
			if !waited {
				waited = true
				d.opts.Logger.Info("instabox: waiting for a machine transition", "tenant", tid, "state", m.State)
			}
			if m.OperationID != nil {
				opID = *m.OperationID
			}
			if err := d.waitSettled(ctx, m); err != nil {
				return nil, err
			}
			continue
		}
		if errors.Is(err, ib.ErrConflict) {
			// Another operation runs (409 operation_conflict): wait.
			if !waited {
				waited = true
				d.opts.Logger.Info("instabox: another operation runs on the machine; waiting", "tenant", tid, "action", action, "err", err)
			}
			if err := sleep(ctx, 500*time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, d.fail(tid, err)
		}
		opID = op.ID
		d.opts.Logger.Info("instabox: asked for a "+action+" of the machine", "tenant", tid, "machine", id, "operation", op.ID)
		if _, err := d.opts.Client.WaitOperation(ctx, op); err != nil {
			return nil, d.fail(tid, err)
		}
	}
}

// logUp reports a machine that runs after action. A resume that could not
// bring the processes back booted the machine fresh: terminals and
// desktop apps of before the pause are gone.
func (d *Driver) logUp(tid string, m *ib.Machine, action string, took time.Duration) {
	attrs := []any{"tenant", tid, "action", action, "took", took.Round(time.Millisecond)}
	if action == "resume" && m.Runtime.ProcessesPreserved != nil && !*m.Runtime.ProcessesPreserved {
		d.opts.Logger.Warn("instabox: the resume did not keep the processes; the machine booted fresh", attrs...)
		return
	}
	d.opts.Logger.Info("instabox: machine running", attrs...)
}

// waitSettled waits for the running operation of m.
func (d *Driver) waitSettled(ctx context.Context, m *ib.Machine) error {
	if m.OperationID != nil && *m.OperationID != "" {
		op, err := d.opts.Client.GetOperation(ctx, *m.OperationID, 0)
		if err == nil {
			_, err = d.opts.Client.WaitOperation(ctx, op)
		}
		if err != nil && !errors.Is(err, ib.ErrNotFound) {
			// A failed operation leaves a state that the next round reads.
			var apiErr *ib.APIError
			if !errors.As(err, &apiErr) || apiErr.Operation == nil {
				return err
			}
		}
		return nil
	}
	return sleep(ctx, 250*time.Millisecond)
}

// Pause implements vmm.Driver: it closes the link and pauses the machine
// with its memory. A paused machine does not wake on use; Resume brings
// it back.
func (d *Driver) Pause(ctx context.Context, tid string) error {
	t := d.tenant(tid)
	t.op.Lock()
	defer t.op.Unlock()
	id, err := d.machineID(ctx, tid)
	if err != nil {
		return err
	}
	d.detach(tid)
	for {
		m, err := d.opts.Client.GetMachine(ctx, id)
		if err != nil {
			return d.fail(tid, err)
		}
		switch m.State {
		case ib.StateSuspended:
			d.setReported(tid, vmm.StatePaused)
			return nil
		case ib.StateRunning:
			op, err := d.opts.Client.PauseMachine(ctx, id)
			if errors.Is(err, ib.ErrConflict) {
				if err := sleep(ctx, 500*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			if err == nil {
				_, err = d.opts.Client.WaitOperation(ctx, op)
			}
			if err != nil {
				return d.fail(tid, err)
			}
			d.setReported(tid, vmm.StatePaused)
			return nil
		case ib.StateStopped, ib.StateError, ib.StateDeleted:
			return fmt.Errorf("%w: pause while the machine is %s", vmm.ErrConflict, m.State)
		default:
			if err := d.waitSettled(ctx, m); err != nil {
				return err
			}
		}
	}
}

// Shutdown implements vmm.Driver: it closes the link and stops the
// machine. The machine and its disk stay; a paused machine is resumed
// first, because InstaBox does not stop a paused machine.
func (d *Driver) Shutdown(ctx context.Context, tid string, force bool) error {
	t := d.tenant(tid)
	t.op.Lock()
	defer t.op.Unlock()
	d.detach(tid)
	rec, _, _, err := loadRecord(ctx, d.opts.Store, tid)
	if err != nil || rec.MachineID == "" {
		return err
	}
	for {
		m, err := d.opts.Client.GetMachine(ctx, rec.MachineID)
		if errors.Is(err, ib.ErrNotFound) {
			return nil
		}
		if err != nil {
			return d.fail(tid, err)
		}
		var op *ib.Operation
		switch m.State {
		case ib.StateStopped, ib.StateError, ib.StateDeleted, ib.StateDeleting:
			d.setReported(tid, vmm.StateStopped)
			return nil
		case ib.StateSuspended:
			if m.IdlePaused() {
				_, err = d.opts.Client.WakeMachine(ctx, rec.MachineID)
				if err != nil && !errors.Is(err, ib.ErrConflict) {
					return d.fail(tid, err)
				}
				continue
			}
			op, err = d.opts.Client.ResumeMachine(ctx, rec.MachineID)
		case ib.StateRunning:
			opts := ib.StopOptions{}
			if force {
				opts.TimeoutSeconds = 5
			}
			op, err = d.opts.Client.StopMachine(ctx, rec.MachineID, opts)
		default:
			if err := d.waitSettled(ctx, m); err != nil {
				return err
			}
			continue
		}
		if errors.Is(err, ib.ErrConflict) {
			if err := sleep(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if err == nil {
			_, err = d.opts.Client.WaitOperation(ctx, op)
		}
		if err != nil {
			return d.fail(tid, err)
		}
	}
}

// setReported records a power state that this driver made, so that the
// watcher reports the next change from it. Without it, a resume that
// starts before the watcher saw the pause, and that ends after the
// controller stopped its wait, is not reported: the watcher would see
// RUNNING before and after.
func (d *Driver) setReported(tid string, st vmm.State) {
	t := d.tenant(tid)
	d.mu.Lock()
	t.reported = st
	d.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
