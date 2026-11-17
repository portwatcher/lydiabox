//go:build unix

package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/agentfs/agentfstest"
	"github.com/lydiabox/lydiabox/internal/agentfs/remotefs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/httpx"
	"github.com/lydiabox/lydiabox/internal/storage/files"
)

func init() {
	fileModes["provider"] = func(t *testing.T, d dirs) (*files.Service, string) {
		g := agentfstest.Start(t, d.home, d.workspace)
		return files.NewService(g.FS, files.Options{}), d.home
	}
}

// TestFileAPIWakeTimeout checks the provider mode when the machine does not
// start: the request waits for the wake timeout and answers 503 with a
// clear message, and the files lease is released.
func TestFileAPIWakeTimeout(t *testing.T) {
	var g *agentfstest.Guest
	e := newEnvWith(t, func(t *testing.T, d dirs) (*files.Service, string) {
		g = agentfstest.Start(t, d.home, d.workspace)
		return files.NewService(g.FS, files.Options{}), d.home
	})
	g.Waker.Set(time.Hour, nil)
	start := time.Now()
	resp, body := e.do(t, "GET", "/api/v1/fs/workspace/list", "", nil, nil)
	if resp.StatusCode != 503 || errCode(t, body) != "unavailable" || !strings.Contains(string(body), "did not start within") {
		t.Fatalf("list while the machine does not wake: %d %s", resp.StatusCode, body)
	}
	if d := time.Since(start); d < time.Second || d > 30*time.Second {
		t.Fatalf("wake timeout took %s", d)
	}
	if g.Waker.Leases(remotefs.LeaseFiles) != 0 {
		t.Fatal("the files lease was not released")
	}

	g.Waker.Set(0, errors.New("boot failed"))
	resp, body = e.do(t, "GET", "/api/v1/fs/home/text?path=x", "", nil, nil)
	if resp.StatusCode != 503 || !strings.Contains(string(body), "boot failed") {
		t.Fatalf("read while the boot fails: %d %s", resp.StatusCode, body)
	}

	// An invalid path fails on the host without waking the machine.
	wakes := g.Waker.Wakes()
	resp, _ = e.do(t, "GET", "/api/v1/fs/workspace/list?path=..%2Fx", "", nil, nil)
	if resp.StatusCode != 400 || g.Waker.Wakes() != wakes {
		t.Fatalf("invalid path: %d, wakes %d -> %d", resp.StatusCode, wakes, g.Waker.Wakes())
	}
}

// A file list that the browser cancels while the machine wakes is a 499,
// not a 500 logged at the error level.
func TestFileListClientGoneIsNotServerError(t *testing.T) {
	g := agentfstest.Start(t, t.TempDir(), t.TempDir())
	g.Waker.Set(time.Hour, nil)
	h, buf := newLoggedServer(t, api.FilesRoutes{Handler: files.NewHandler(files.NewService(g.FS, files.Options{}))})
	ctx, cancel := context.WithCancel(context.Background())
	req := authed(http.MethodGet, "/api/v1/fs/workspace/list").WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	for g.Waker.Wakes() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if rec.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, httpx.StatusClientClosedRequest, rec.Body)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("a closed request was logged as an error:\n%s", buf)
	}
}
