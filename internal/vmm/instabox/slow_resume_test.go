//go:build unix

package instabox_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest"
	"github.com/lydiabox/lydiabox/internal/vmm"
	"github.com/lydiabox/lydiabox/internal/vmm/guestclient"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

// slowRig is a controller on the InstaBox driver whose resume takes
// resumeDelay at the fake server. bootTimeout is the guest-ready bound and
// providerTimeout the provider bound.
type slowRig struct {
	e    *env
	d    *vmmib.Driver
	c    *vmm.Controller
	logs *logBuffer
	mu   sync.Mutex
	seen []vmm.State
}

func newSlowRig(t *testing.T, resumeDelay, bootTimeout, providerTimeout time.Duration) *slowRig {
	t.Helper()
	r := &slowRig{logs: &logBuffer{}}
	r.e = newEnvFake(t, nil, func(o *instaboxtest.Options) { o.ResumeDelay = resumeDelay })
	r.d = r.e.driver(t, func(o *vmmib.Options) {
		o.ProviderTimeout = providerTimeout
		o.PollInterval = 100 * time.Millisecond
		o.Logger = slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	})
	gc := guestclient.New(guestclient.Options{Dialer: r.d, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { gc.Reset(tenant) })
	c, err := vmm.NewController(vmm.Options{
		Driver: r.d, BootTimeout: bootTimeout, Logger: slog.New(slog.DiscardHandler),
		Spec: func(context.Context, string) (vmm.Spec, error) { return vmm.Spec{CPUs: 2, MemMB: 4096}, nil },
		Ready: func(ctx context.Context, tid string) error {
			_, err := gc.Ping(ctx, tid)
			return err
		},
		OnStateChange: func(_ string, st vmm.State) {
			r.mu.Lock()
			r.seen = append(r.seen, st)
			r.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	// Boot the machine, then pause it like the idle timer does.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.EnsureRunning(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err := c.Hibernate(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.seen = nil
	r.mu.Unlock()
	return r
}

func (r *slowRig) states() []vmm.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]vmm.State(nil), r.seen...)
}

// TestSlowResumeIsNoFailure: a resume that takes longer than the
// guest-ready bound (the old bound of the full boot) works, and the state
// stays BOOTING until the machine runs.
func TestSlowResumeIsNoFailure(t *testing.T) {
	checkLeaks(t)
	r := newSlowRig(t, 1500*time.Millisecond, 500*time.Millisecond, 20*time.Second)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- r.c.EnsureRunning(context.Background(), tenant) }()

	// While InstaBox resumes the machine, the state is BOOTING and no
	// error shows.
	time.Sleep(900 * time.Millisecond)
	if st := r.c.Status(tenant); st.State != string(vmm.StateBooting) || st.LastError != "" {
		t.Fatalf("status during a slow resume: %+v", st)
	}
	if err := <-done; err != nil {
		t.Fatalf("slow resume: %v", err)
	}
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("the resume took %v; the fake did not delay it", took)
	}
	if st := r.c.Status(tenant); st.State != string(vmm.StateRunning) || st.LastError != "" {
		t.Fatalf("status after a slow resume: %+v", st)
	}
	if got := r.states(); len(got) != 2 || got[0] != vmm.StateBooting || got[1] != vmm.StateRunning {
		t.Fatalf("state changes %v, want [BOOTING RUNNING]", got)
	}
	if !strings.Contains(r.logs.String(), `msg="instabox: asked for a resume of the machine"`) {
		t.Fatalf("no log line for the start of the resume:\n%s", r.logs.String())
	}
}

// TestResumeAtProviderBound: a resume that takes longer than the provider
// bound fails with a clear error, does not stop the machine, and logs the
// action, the operation, and the time. When InstaBox finishes the resume,
// the state watcher adopts the machine and the error clears.
func TestResumeAtProviderBound(t *testing.T) {
	checkLeaks(t)
	r := newSlowRig(t, 1500*time.Millisecond, 5*time.Second, 300*time.Millisecond)
	err := r.c.EnsureRunning(context.Background(), tenant)
	var pt *vmm.ProviderTimeoutError
	if !errors.As(err, &pt) || pt.Action != "resume" || !errors.Is(err, vmm.ErrUnavailable) {
		t.Fatalf("error %v (%T), want a ProviderTimeoutError of the resume", err, err)
	}
	if strings.Contains(err.Error(), "deadline") || !strings.Contains(err.Error(), "did not resume the machine in 300ms") {
		t.Fatalf("error text %q", err)
	}
	st := r.c.Status(tenant)
	if st.LastError != err.Error() {
		t.Fatalf("lastError %q, want %q", st.LastError, err.Error())
	}
	if n := r.e.count(http.MethodPost, "/v1/machines/"+r.e.machineID(t)+"/stop"); n != 0 {
		t.Fatalf("%d stop requests after the provider bound, want none", n)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "action=resume") || !strings.Contains(logs, "operation=op_") || !strings.Contains(logs, "waited=") {
		t.Fatalf("no warning with the action, the operation, and the time:\n%s", logs)
	}

	// InstaBox ends the resume; the watcher reports the machine.
	deadline := time.Now().Add(15 * time.Second)
	for {
		st = r.c.Status(tenant)
		if st.State == string(vmm.StateRunning) && st.LastError == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the watcher did not adopt the resumed machine: %+v (driver state %s)\n%s", st, state(t, r.d), r.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := r.c.EnsureRunning(context.Background(), tenant); err != nil {
		t.Fatalf("use after the adopted resume: %v", err)
	}
	if n := r.e.count(http.MethodPost, "/v1/machines/"+r.e.machineID(t)+"/stop"); n != 0 {
		t.Fatalf("%d stop requests, want none", n)
	}
}
