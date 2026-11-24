//go:build unix

package instabox_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
	"github.com/portwatcher/lydiabox/internal/vmm"
	"github.com/portwatcher/lydiabox/internal/vmm/guestclient"
	vmmib "github.com/portwatcher/lydiabox/internal/vmm/instabox"
)

// pauseRig is a running controller on the InstaBox driver whose suspend
// takes suspendDelay at the fake server (the machine shows "running" with
// desiredState "suspended" meanwhile, like the real service).
type pauseRig struct {
	e    *env
	d    *vmmib.Driver
	c    *vmm.Controller
	gc   *guestclient.Client
	logs *logBuffer
}

func newPauseRig(t *testing.T, suspendDelay, providerTimeout time.Duration) *pauseRig {
	t.Helper()
	r := &pauseRig{logs: &logBuffer{}}
	r.e = newEnvFake(t, nil, func(o *instaboxtest.Options) { o.SuspendDelay = suspendDelay })
	r.d = r.e.driver(t, func(o *vmmib.Options) {
		o.ProviderTimeout = providerTimeout
		o.PollInterval = 100 * time.Millisecond
		o.Logger = slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	})
	r.gc = guestclient.New(guestclient.Options{Dialer: r.d, Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(func() { r.gc.Reset(tenant) })
	c, err := vmm.NewController(vmm.Options{
		Driver: r.d, BootTimeout: 10 * time.Second, Logger: slog.New(slog.DiscardHandler),
		Spec: func(context.Context, string) (vmm.Spec, error) { return vmm.Spec{CPUs: 2, MemMB: 4096}, nil },
		Ready: func(ctx context.Context, tid string) error {
			_, err := r.gc.Ping(ctx, tid)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.EnsureRunning(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	return r
}

// waitPaused waits until the controller shows PAUSED without an error.
func (r *pauseRig) waitPaused(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		st := r.c.Status(tenant)
		if st.State == string(vmm.StatePaused) && st.LastError == "" && st.Transition == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the VM did not become PAUSED: %+v\n%s", st, r.logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// checkPausing checks the status while the provider still suspends.
func (r *pauseRig) checkPausing(t *testing.T) {
	t.Helper()
	st := r.c.Status(tenant)
	if st.State != string(vmm.StateRunning) || st.Transition != vmm.TransitionPausing || st.LastError != "" {
		t.Fatalf("status during a slow pause: %+v", st)
	}
	if r.c.Serving(tenant) || !r.c.Paused(tenant) {
		t.Fatalf("during a slow pause: serving %v paused %v", r.c.Serving(tenant), r.c.Paused(tenant))
	}
}

func (r *pauseRig) ping(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.gc.Ping(ctx, tenant); err != nil {
		t.Fatalf("guest ping: %v\n%s", err, r.logs.String())
	}
}

// TestPauseOutlivesTheCaller: a caller that goes away during the pause
// does not cancel it. The status shows the pause until the machine is
// PAUSED, and no error shows.
func TestPauseOutlivesTheCaller(t *testing.T) {
	checkLeaks(t)
	r := newPauseRig(t, 1500*time.Millisecond, 20*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	if err := r.c.Hibernate(ctx, tenant); !errors.Is(err, vmm.ErrPausing) {
		t.Fatalf("hibernate with a cancelled caller: %v, want ErrPausing", err)
	}
	time.Sleep(300 * time.Millisecond)
	r.checkPausing(t)
	r.waitPaused(t)
	if st := state(t, r.d); st != vmm.StatePaused {
		t.Fatalf("driver state %s", st)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, `msg="instabox: asked for a suspend of the machine"`) || !strings.Contains(logs, `msg="instabox: machine paused"`) {
		t.Fatalf("no log lines for the suspend:\n%s", logs)
	}
	if strings.Contains(logs, "level=WARN") {
		t.Fatalf("a warning for a pause that worked:\n%s", logs)
	}
}

// TestHibernateRouteAnswersWhileThePauseContinues: the route stops its
// wait at its bound and answers 202 with the transition; the VM becomes
// PAUSED later.
func TestHibernateRouteAnswersWhileThePauseContinues(t *testing.T) {
	checkLeaks(t)
	r := newPauseRig(t, 1500*time.Millisecond, 20*time.Second)
	srv, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: api.NewAuthenticator("0123456789abcdef0123"), TenantID: tenant},
		api.VMRoutes{Controller: r.c, HibernateWait: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/vm/hibernate", nil)
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out api.VMDetail
	err = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 1200*time.Millisecond {
		t.Fatalf("the route waited %v, want about its bound", took)
	}
	if resp.StatusCode != http.StatusAccepted || out.State != api.VMRunning || out.Transition != vmm.TransitionPausing || out.LastError != "" {
		t.Fatalf("hibernate answer %d %+v", resp.StatusCode, out)
	}
	r.waitPaused(t)
}

// TestWakeDuringASlowPause: a wake asked for during a slow pause waits
// for the pause and then resumes the machine.
func TestWakeDuringASlowPause(t *testing.T) {
	checkLeaks(t)
	r := newPauseRig(t, time.Second, 20*time.Second)
	paused := make(chan error, 1)
	start := time.Now()
	go func() { paused <- r.c.Hibernate(context.Background(), tenant) }()
	time.Sleep(200 * time.Millisecond)
	r.checkPausing(t)
	if err := r.c.EnsureRunning(context.Background(), tenant); err != nil {
		t.Fatalf("wake during a slow pause: %v", err)
	}
	if err := <-paused; err != nil {
		t.Fatalf("hibernate: %v", err)
	}
	if took := time.Since(start); took < time.Second {
		t.Fatalf("the wake returned after %v, before the pause ended", took)
	}
	if st := r.c.Status(tenant); st.State != string(vmm.StateRunning) || st.LastError != "" || st.Transition != "" {
		t.Fatalf("status after the wake: %+v", st)
	}
	id := r.e.machineID(t)
	if n := r.e.count(http.MethodPost, "/v1/machines/"+id+"/suspend"); n != 1 {
		t.Fatalf("%d suspend requests, want 1", n)
	}
	if n := r.e.count(http.MethodPost, "/v1/machines/"+id+"/resume"); n != 1 {
		t.Fatalf("%d resume requests, want 1", n)
	}
	r.ping(t)
}

// TestFailedPauseKeepsAWorkingGuest: a pause that InstaBox refuses
// leaves the machine RUNNING with a guest link that works, and a clear
// error.
func TestFailedPauseKeepsAWorkingGuest(t *testing.T) {
	checkLeaks(t)
	r := newPauseRig(t, 0, 20*time.Second)
	id := r.e.machineID(t)
	r.e.fake.InjectError(http.MethodPost, "/v1/machines/"+id+"/suspend", http.StatusInternalServerError, "internal", false, 0, 1)
	err := r.c.Hibernate(context.Background(), tenant)
	if err == nil || strings.Contains(err.Error(), "context") {
		t.Fatalf("hibernate error %v", err)
	}
	st := r.c.Status(tenant)
	if st.State != string(vmm.StateRunning) || st.Transition != "" || st.LastError != err.Error() {
		t.Fatalf("status after a failed pause: %+v", st)
	}
	if !r.c.Serving(tenant) {
		t.Fatal("the VM is not serving after a failed pause")
	}
	r.ping(t)
	logs := r.logs.String()
	if !strings.Contains(logs, `msg="instabox: the machine did not pause"`) || !strings.Contains(logs, "waited=") {
		t.Fatalf("no warning for the failed pause:\n%s", logs)
	}
	// The next pause works.
	if err := r.c.Hibernate(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	r.waitPaused(t)
}

// TestPauseAtProviderBound: a pause that takes longer than the provider
// bound does not show a failure when InstaBox still suspends the
// machine: the state is PAUSED, and a wake waits for the suspend and then
// resumes the machine.
func TestPauseAtProviderBound(t *testing.T) {
	checkLeaks(t)
	r := newPauseRig(t, 1500*time.Millisecond, 400*time.Millisecond)
	if err := r.c.Hibernate(context.Background(), tenant); err != nil {
		t.Fatalf("hibernate at the provider bound: %v", err)
	}
	if st := r.c.Status(tenant); st.State != string(vmm.StatePaused) || st.LastError != "" {
		t.Fatalf("status at the provider bound: %+v", st)
	}
	logs := r.logs.String()
	if !strings.Contains(logs, `msg="instabox: the machine did not pause"`) || !strings.Contains(logs, "operation=op_") {
		t.Fatalf("no warning with the operation:\n%s", logs)
	}
	// The resume has a bound of its own; the suspend ends first.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		err := r.c.EnsureRunning(ctx, tenant)
		if err == nil {
			break
		}
		var pt *vmm.ProviderTimeoutError
		if !errors.As(err, &pt) || ctx.Err() != nil {
			t.Fatalf("wake after the provider bound: %v", err)
		}
	}
	if st := r.c.Status(tenant); st.State != string(vmm.StateRunning) || st.LastError != "" {
		t.Fatalf("status after the wake: %+v", st)
	}
	r.ping(t)
}
