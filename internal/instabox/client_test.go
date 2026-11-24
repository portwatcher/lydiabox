package instabox_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

func TestNewClientValidates(t *testing.T) {
	if _, err := instabox.NewClient(instabox.Options{}); err == nil {
		t.Fatal("no API key accepted")
	}
	if _, err := instabox.NewClient(instabox.Options{APIKey: "ibk_x", BaseURL: "ftp://x"}); err == nil {
		t.Fatal("ftp base URL accepted")
	}
	c, err := instabox.NewClient(instabox.Options{APIKey: "ibk_x", BaseURL: "https://api.example.test/v1/"})
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL() != "https://api.example.test" {
		t.Fatalf("BaseURL = %s", c.BaseURL())
	}
}

func TestAPIKeyNeverShown(t *testing.T) {
	const key = "ibk_secretsecretsecret"
	r := newRig(t, instaboxtest.Options{APIKey: key})
	ctx := ctxT(t)
	m := r.running(t, "keycheck")
	r.fake.InjectError(http.MethodGet, "/v1/machines/", http.StatusServiceUnavailable, "host_unavailable", true, 0, 1)
	if _, err := r.client.GetMachine(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	_, err := r.client.GetMachine(ctx, "nope")
	for _, s := range []string{fmt.Sprint(r.client), fmt.Sprintf("%#v", r.client), fmt.Sprintf("%+v", r.client), err.Error(), r.log.String()} {
		if strings.Contains(s, key) {
			t.Fatalf("API key leaked in %q", s)
		}
	}
	if !strings.Contains(r.log.String(), "status=503") {
		t.Fatalf("debug log has no attempt lines: %s", r.log.String())
	}
	reqs := r.fake.Requests()
	if got := reqs[len(reqs)-1].Header.Get("X-API-Key"); got != key {
		t.Fatalf("X-API-Key = %q", got)
	}
}

func TestUnauthorized(t *testing.T) {
	r := newRig(t, instaboxtest.Options{}, func(o *instabox.Options) { o.APIKey = "ibk_wrong" })
	_, err := r.client.ListMachines(ctxT(t), instabox.ListMachinesOptions{})
	if !errors.Is(err, instabox.ErrUnauthorized) || instabox.CodeOf(err) != "invalid_key" {
		t.Fatalf("err = %v, want invalid_key", err)
	}
	var apiErr *instabox.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.RequestID == "" {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if len(r.fake.Requests()) != 1 {
		t.Fatal("401 was retried")
	}
}

func TestIdempotencyKeys(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	m := r.running(t, "idem")
	r.done(t)(r.client.StopMachine(ctx, m.ID, instabox.StopOptions{}))
	r.done(t)(r.client.StartMachine(ctx, m.ID, instabox.StartOptions{}))
	if _, err := r.client.ListMachines(ctx, instabox.ListMachinesOptions{}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, req := range r.fake.Requests() {
		key := req.Header.Get("Idempotency-Key")
		mutating := req.Method != http.MethodGet
		if mutating != (key != "") {
			t.Fatalf("%s %s: Idempotency-Key %q", req.Method, req.Path, key)
		}
		if key != "" && seen[key] {
			t.Fatalf("key %s reused across calls", key)
		}
		seen[key] = true
	}
}

func TestIdempotencyReplayAndConflict(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := instabox.WithIdempotencyKey(ctxT(t), "create-once")
	op1, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "once"})
	if err != nil {
		t.Fatal(err)
	}
	op2, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "once"})
	if err != nil {
		t.Fatal(err)
	}
	if op1.ID != op2.ID || r.fake.Replays() != 1 {
		t.Fatalf("replay: %s vs %s, replays %d", op1.ID, op2.ID, r.fake.Replays())
	}
	_, err = r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "other"})
	if !errors.Is(err, instabox.ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want idempotency conflict", err)
	}
	if !strings.HasPrefix(instabox.NewIdempotencyKey(), "lbx-") || len(instabox.NewIdempotencyKey()) != 36 {
		t.Fatal("bad generated key")
	}
}

func TestRetryTransientGet(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	m := r.running(t, "retry")
	r.fake.InjectError(http.MethodGet, "/v1/machines/", http.StatusServiceUnavailable, "host_unavailable", true, 0, 2)
	if _, err := r.client.GetMachine(ctxT(t), m.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(r.Waits()); n != 2 {
		t.Fatalf("waits = %d, want 2", n)
	}
}

func TestRetryRespectsRetryAfter(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	r.fake.InjectError(http.MethodGet, "/v1/machines", http.StatusTooManyRequests, "rate_limited", true, 3, 1)
	if _, err := r.client.ListMachines(ctxT(t), instabox.ListMachinesOptions{}); err != nil {
		t.Fatal(err)
	}
	if w := r.Waits(); len(w) != 1 || w[0] != 3*time.Second {
		t.Fatalf("waits = %v, want [3s]", w)
	}
	// A Retry-After over MaxRetryDelay goes back to the caller.
	r.fake.InjectError(http.MethodGet, "/v1/machines", http.StatusServiceUnavailable, "no_capacity", true, 60, 1)
	_, err := r.client.ListMachines(ctxT(t), instabox.ListMachinesOptions{})
	var apiErr *instabox.APIError
	if !errors.As(err, &apiErr) || !errors.Is(err, instabox.ErrNoCapacity) || apiErr.RetryAfter != time.Minute {
		t.Fatalf("err = %v", err)
	}
}

func TestNoRetryOnPermanentError(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	r.fake.InjectError(http.MethodGet, "/v1/machines", http.StatusServiceUnavailable, "host_unavailable", false, 0, 1)
	_, err := r.client.ListMachines(ctxT(t), instabox.ListMachinesOptions{})
	if !errors.Is(err, instabox.ErrUnavailable) || instabox.IsRetryable(err) {
		t.Fatalf("err = %v", err)
	}
	if len(r.Waits()) != 0 {
		t.Fatal("non-retryable error was retried")
	}
}

func TestRetryGivesUp(t *testing.T) {
	r := newRig(t, instaboxtest.Options{}, func(o *instabox.Options) { o.MaxRetries = 2 })
	r.fake.InjectError(http.MethodGet, "/v1/machines", http.StatusInternalServerError, "internal", true, 0, 10)
	_, err := r.client.ListMachines(ctxT(t), instabox.ListMachinesOptions{})
	if instabox.CodeOf(err) != "internal" || len(r.fake.Requests()) != 3 {
		t.Fatalf("err = %v after %d requests", err, len(r.fake.Requests()))
	}
}

func TestRetryDisconnectReplaysCreate(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	// The server creates the machine, then the connection drops before
	// the answer: the retry sends the same key and gets the same
	// operation, so there is one machine.
	r.fake.InjectDisconnect(http.MethodPost, "/v1/machines", true, 1)
	op, err := r.client.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "dropped"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := r.client.ListAllMachines(ctx, instabox.ListMachinesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != op.MachineID || r.fake.Replays() != 1 {
		t.Fatalf("machines %d, replays %d", len(all), r.fake.Replays())
	}
}

func TestNonJSONGatewayErrorRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshots":[]}`))
	}))
	defer srv.Close()
	c, err := instabox.NewClient(instabox.Options{BaseURL: srv.URL, APIKey: "ibk_x", RetryBaseDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListSnapshots(ctxT(t)); err != nil || calls != 2 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}

func TestErrorSentinels(t *testing.T) {
	cases := []struct {
		e    instabox.APIError
		want error
	}{
		{instabox.APIError{Status: 404, ErrorDetail: instabox.ErrorDetail{Code: "not_found"}}, instabox.ErrNotFound},
		{instabox.APIError{Status: 403, ErrorDetail: instabox.ErrorDetail{Code: "missing_scope"}}, instabox.ErrForbidden},
		{instabox.APIError{Status: 402, ErrorDetail: instabox.ErrorDetail{Code: "credit_exhausted"}}, instabox.ErrPaymentRequired},
		{instabox.APIError{Status: 429, ErrorDetail: instabox.ErrorDetail{Code: "quota_exceeded"}}, instabox.ErrQuota},
		{instabox.APIError{Status: 409, ErrorDetail: instabox.ErrorDetail{Code: "operation_conflict"}}, instabox.ErrConflict},
		{instabox.APIError{Status: 409, ErrorDetail: instabox.ErrorDetail{Code: "machine_not_running"}}, instabox.ErrNotRunning},
		{instabox.APIError{Status: 503, ErrorDetail: instabox.ErrorDetail{Code: "no_capacity"}}, instabox.ErrNoCapacity},
		{instabox.APIError{Status: 502}, instabox.ErrUnavailable},
	}
	for _, tc := range cases {
		if !errors.Is(&tc.e, tc.want) {
			t.Errorf("%v does not match %v", &tc.e, tc.want)
		}
	}
	if errors.Is(&instabox.APIError{Status: 503, ErrorDetail: instabox.ErrorDetail{Code: "no_capacity"}}, instabox.ErrUnavailable) {
		t.Error("no_capacity matches ErrUnavailable")
	}
}
