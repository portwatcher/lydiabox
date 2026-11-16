//go:build unix

package instabox_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/vmm"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

// controller returns a VM controller over d whose readiness probe pings
// the guest daemon through the link.
func controller(t *testing.T, d *vmmib.Driver) *vmm.Controller {
	t.Helper()
	gc := guestclient.New(guestclient.Options{Dialer: d, Logger: slog.New(slog.DiscardHandler)})
	ctl, err := vmm.NewController(vmm.Options{Driver: d, Logger: slog.New(slog.DiscardHandler),
		Spec: func(context.Context, string) (vmm.Spec, error) { return vmm.Spec{CPUs: 2, MemMB: 4096}, nil },
		Ready: func(ctx context.Context, tid string) error {
			_, err := gc.Ping(ctx, tid)
			return err
		},
		OnStateChange: func(tid string, st vmm.State) {
			if st == vmm.StateStopped {
				gc.Reset(tid)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gc.Reset(tenant) })
	return ctl
}

// A machine that the user stopped stays stopped when a desktop, terminal,
// or file request wants it (EnsureRunning); an explicit start starts it.
func TestUserStopIsNotUndoneByAWakeOnDemand(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	d := e.driver(t)
	ctl := controller(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := ctl.Start(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	id := e.machineID(t)
	if err := ctl.Stop(ctx, tenant, false); err != nil {
		t.Fatal(err)
	}
	starts := e.count(http.MethodPost, "/v1/machines/"+id+"/start")
	wakes := e.count(http.MethodPost, "/v1/machines/"+id+"/wake")

	if err := ctl.EnsureRunning(ctx, tenant); !errors.Is(err, vmm.ErrStopped) {
		t.Fatalf("wake on demand after a user stop: %v", err)
	}
	if _, err := d.DialGuest(ctx, tenant, 9000); !errors.Is(err, vmm.ErrUnavailable) {
		t.Fatalf("dial of a stopped machine: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if e.fake.State(id) != "stopped" || ctl.State(tenant) != vmm.StateStopped {
		t.Fatalf("machine %s, controller %s: the stop was undone", e.fake.State(id), ctl.State(tenant))
	}
	if e.count(http.MethodPost, "/v1/machines/"+id+"/start") != starts || e.count(http.MethodPost, "/v1/machines/"+id+"/wake") != wakes {
		t.Fatal("a request started the stopped machine")
	}

	// Stop, start, stop: each explicit start works.
	for i := 0; i < 2; i++ {
		if err := ctl.Start(ctx, tenant); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if e.fake.State(id) != "running" {
			t.Fatalf("start %d: machine %s", i, e.fake.State(id))
		}
		if err := ctl.Stop(ctx, tenant, false); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
	}
}

// A machine that InstaBox paused by itself (idle pause) is not a user
// stop: a request wakes it.
func TestIdlePausedMachineWakesOnDemand(t *testing.T) {
	checkLeaks(t)
	e := newEnv(t)
	d := e.driver(t)
	ctl := controller(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := ctl.Start(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	id := e.machineID(t)
	if !e.fake.IdlePause(id) {
		t.Fatal("idle pause")
	}
	if err := ctl.Refresh(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if ctl.State(tenant) != vmm.StatePaused {
		t.Fatalf("state %s", ctl.State(tenant))
	}
	if err := ctl.EnsureRunning(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if e.count(http.MethodPost, "/v1/machines/"+id+"/wake") != 1 || e.fake.State(id) != "running" {
		t.Fatalf("machine %s after a wake on demand", e.fake.State(id))
	}
	if err := ctl.Stop(ctx, tenant, true); err != nil {
		t.Fatal(err)
	}
}
