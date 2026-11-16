package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeVM struct {
	state    string
	wakeErr  error
	stopArgs []bool
}

func (f *fakeVM) VMStatus(context.Context, string) (VMStatus, error) {
	return VMStatus{State: f.state, Driver: "docker"}, nil
}

func (f *fakeVM) Status(string) VMDetail {
	return VMDetail{State: f.state, Driver: "docker", CPUs: 4, MemMB: 8192, Leases: map[string]int{"pty": 0, "desktop": 0}}
}

func (f *fakeVM) Start(ctx context.Context, _ string) error {
	if f.wakeErr != nil {
		return f.wakeErr
	}
	f.state = VMRunning
	return nil
}

func (f *fakeVM) Hibernate(context.Context, string) error {
	if f.state != VMRunning {
		return fmt.Errorf("%w: hibernate while %s", ErrVMConflict, f.state)
	}
	f.state = VMPaused
	return nil
}

func (f *fakeVM) Stop(_ context.Context, _ string, force bool) error {
	f.stopArgs = append(f.stopArgs, force)
	if f.state == VMStopped {
		return fmt.Errorf("%w: stop while STOPPED", ErrVMConflict)
	}
	f.state = VMStopped
	return nil
}

func vmServer(t *testing.T, vm *fakeVM) *httptest.Server {
	t.Helper()
	auth := NewAuthenticator("0123456789abcdef0123")
	srv, err := NewServer(Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default"}, VMRoutes{Controller: vm})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func vmDo(t *testing.T, ts *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer 0123456789abcdef0123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestVMRoutes(t *testing.T) {
	vm := &fakeVM{state: VMStopped}
	ts := vmServer(t, vm)
	if code, out := vmDo(t, ts, "GET", "/api/v1/vm/status", ""); code != 200 || out["state"] != "STOPPED" || out["memMb"].(float64) != 8192 {
		t.Fatalf("status %d %v", code, out)
	}
	if code, out := vmDo(t, ts, "POST", "/api/v1/vm/hibernate", ""); code != 409 || out["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("hibernate while stopped %d %v", code, out)
	}
	if code, out := vmDo(t, ts, "POST", "/api/v1/vm/wake", ""); code != 200 || out["state"] != "RUNNING" {
		t.Fatalf("wake %d %v", code, out)
	}
	if code, out := vmDo(t, ts, "POST", "/api/v1/vm/hibernate", ""); code != 200 || out["state"] != "PAUSED" {
		t.Fatalf("hibernate %d %v", code, out)
	}
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/stop", `{"force":true}`); code != 200 || !vm.stopArgs[0] {
		t.Fatalf("stop %d %v", code, vm.stopArgs)
	}
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/stop", ""); code != 409 || vm.stopArgs[1] {
		t.Fatalf("stop while stopped %d %v", code, vm.stopArgs)
	}
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/stop", `{"force":true,"x":1}`); code != 400 {
		t.Fatalf("unknown field %d", code)
	}
	vm.wakeErr = context.DeadlineExceeded
	if code, out := vmDo(t, ts, "POST", "/api/v1/vm/wake", ""); code != 504 || out["error"].(map[string]any)["code"] != "timeout" {
		t.Fatalf("wake timeout %d %v", code, out)
	}
	vm.wakeErr = fmt.Errorf("%w: guest down", ErrVMUnavailable)
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/wake", ""); code != 503 {
		t.Fatalf("wake unavailable %d", code)
	}
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/stop", `{"resetRoot":true}`); code != 400 {
		t.Fatalf("resetRoot without support %d", code)
	}
}

type resetVM struct {
	fakeVM
	resets int
}

func (f *resetVM) ResetRoot(context.Context, string) error {
	if f.state != VMStopped {
		return fmt.Errorf("%w: reset while %s", ErrVMConflict, f.state)
	}
	f.resets++
	return nil
}

func TestVMStopResetRoot(t *testing.T) {
	vm := &resetVM{fakeVM: fakeVM{state: VMRunning}}
	auth := NewAuthenticator("0123456789abcdef0123")
	srv, err := NewServer(Options{Addr: "127.0.0.1:0", Auth: auth, TenantID: "default"}, VMRoutes{Controller: vm})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	if code, out := vmDo(t, ts, "POST", "/api/v1/vm/stop", `{"resetRoot":true}`); code != 200 || out["state"] != VMStopped || vm.resets != 1 {
		t.Fatalf("stop and reset %d %v resets=%d", code, out, vm.resets)
	}
	// Already stopped: the reset still runs.
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/stop", `{"resetRoot":true}`); code != 200 || vm.resets != 2 {
		t.Fatalf("reset while stopped %d resets=%d", code, vm.resets)
	}
	if code, _ := vmDo(t, ts, "POST", "/api/v1/vm/stop", ""); code != 409 || vm.resets != 2 {
		t.Fatalf("plain stop while stopped %d", code)
	}
}
