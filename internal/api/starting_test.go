package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/api"
	"github.com/portwatcher/lydiabox/internal/httpx"
)

// A 503 of a request that stopped its wait for a VM start (the start
// continues) gets the code vm_starting, Retry-After, and is logged as an expected state; another
// 503 gets neither.
func TestStartingVMAnswersRetryAfter(t *testing.T) {
	h, buf := newLoggedServer(t, api.RegistrarFunc(func(r api.Router) {
		r.HandleFunc("GET /api/v1/test/starting", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), time.Millisecond)
			defer cancel()
			<-ctx.Done()
			httpx.NoteVMStarting(ctx)
			httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, api.ErrVMStarting.Error())
		})
		r.HandleFunc("GET /api/v1/test/down", func(w http.ResponseWriter, _ *http.Request) {
			httpx.WriteError(w, http.StatusServiceUnavailable, httpx.CodeUnavailable, "magpie is not running")
		})
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(http.MethodGet, "/api/v1/test/starting"))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("starting: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), `"code":"vm_starting"`) {
		t.Fatalf("starting body %s, want the code vm_starting", rec.Body.String())
	}
	if !strings.Contains(buf.String(), `msg="request refused"`) || strings.Contains(buf.String(), "request failed") {
		t.Fatalf("log of a VM that still starts:\n%s", buf.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authed(http.MethodGet, "/api/v1/test/down"))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("down: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("down body %s, want the code unavailable", rec.Body.String())
	}
}
