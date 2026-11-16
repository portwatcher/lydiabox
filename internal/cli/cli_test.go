package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/storage"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(context.Background(), args, Env{Stdout: &out, Stderr: &errb})
	return code, out.String(), errb.String()
}

func isolate(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LYDIABOX_DATA_DIR", t.TempDir())
	for _, k := range []string{"LYDIABOX_S3_ENDPOINT", "LYDIABOX_S3_BUCKET", "LYDIABOX_S3_ACCESS_KEY", "LYDIABOX_S3_SECRET_KEY", "LYDIABOX_AUTH_TOKEN", "LYDIABOX_CONFIG"} {
		t.Setenv(k, "")
	}
}

func TestUsageAndVersion(t *testing.T) {
	isolate(t)
	if code, out, _ := run(t); code != 2 || !strings.Contains(out, "Usage: lydiabox") {
		t.Fatalf("no args: %d %q", code, out)
	}
	if code, _, _ := run(t, "--help"); code != 0 {
		t.Fatalf("--help: %d", code)
	}
	if code, _, errOut := run(t, "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
	if code, out, _ := run(t, "version"); code != 0 || !strings.HasPrefix(out, "lydiabox ") {
		t.Fatalf("version: %d %q", code, out)
	}
	code, out, _ := run(t, "version", "-json")
	var v map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v["version"] == "" {
		t.Fatalf("version -json: %d %q", code, out)
	}
	if code, _, errOut := run(t, "runtime"); code != 2 || !strings.Contains(errOut, "build-rootfs") {
		t.Fatalf("runtime usage: %d %q", code, errOut)
	}
}

func TestUpRequiresS3(t *testing.T) {
	isolate(t)
	code, _, errOut := run(t, "up", "-skip-bootstrap")
	if code != 1 || !strings.Contains(errOut, "LYDIABOX_S3_BUCKET is required") {
		t.Fatalf("up without S3: %d %q", code, errOut)
	}
}

func TestRuntimeStatusAndBuildRootfs(t *testing.T) {
	isolate(t)
	t.Setenv("LYDIABOX_VM_DRIVER", "docker")
	code, out, errOut := run(t, "runtime", "status")
	if code != 0 || !strings.Contains(out, "juicefs") || !strings.Contains(out, "missing") {
		t.Fatalf("runtime status: %d %q %q", code, out, errOut)
	}
	code, _, errOut = run(t, "runtime", "build-rootfs", "-source", t.TempDir())
	if code != 1 || !strings.Contains(errOut, "build-artifacts.sh not found") {
		t.Fatalf("build-rootfs: %d %q", code, errOut)
	}
	code, _, errOut = run(t, "runtime", "fetch-images")
	if code != 1 || !strings.Contains(errOut, "LYDIABOX_ROOTFS_URL") {
		t.Fatalf("fetch-images: %d %q", code, errOut)
	}
	if code, _, errOut = run(t, "runtime", "install-local", "virtiofsd"); code == 0 || !strings.Contains(errOut, "install-local <name> <file>") {
		t.Fatalf("install-local without a file: %d %q", code, errOut)
	}
	built := filepath.Join(t.TempDir(), "virtiofsd-linux-arm64")
	if err := os.WriteFile(built, []byte("\x7fELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = run(t, "runtime", "install-local", "virtiofsd", built)
	if code != 0 || !strings.Contains(out, "virtiofsd installed in") {
		t.Fatalf("install-local: %d %q %q", code, out, errOut)
	}
}

func TestStatusCommand(t *testing.T) {
	isolate(t)
	const token = "status-test-token-0123456789"
	t.Setenv("LYDIABOX_AUTH_TOKEN", token)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/api/v1/status" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		now := time.Now().UTC()
		json.NewEncoder(w).Encode(api.StatusResponse{TenantID: "default", Time: now,
			VM:      api.VMStatus{State: "STOPPED", Driver: "docker"},
			Storage: storage.Status{Mounted: true, LastSyncAt: now.Add(-42 * time.Second)}})
	}))
	defer srv.Close()
	code, out, errOut := run(t, "status", "-addr", srv.URL)
	if code != 0 || !strings.Contains(out, "STOPPED") || !strings.Contains(out, "42s ago") || !strings.Contains(out, "mounted") {
		t.Fatalf("status: %d %q %q", code, out, errOut)
	}
	t.Setenv("LYDIABOX_AUTH_TOKEN", "wrong-token-0123456789abc")
	if code, _, errOut := run(t, "status", "-addr", srv.URL); code != 1 || !strings.Contains(errOut, "401") {
		t.Fatalf("status with wrong token: %d %q", code, errOut)
	}
	t.Setenv("LYDIABOX_AUTH_TOKEN", "")
	if code, _, errOut := run(t, "status"); code != 1 || !strings.Contains(errOut, "no admin token") {
		t.Fatalf("status without token: %d %q", code, errOut)
	}
}

func TestPrintStatusProviderSync(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		st   storage.Status
		want string
	}{
		{storage.Status{Mode: config.StorageProvider}, "S3 sync:  off"},
		{storage.Status{Mode: config.StorageProvider, SyncEnabled: true}, "S3 sync:  not yet"},
		{storage.Status{Mode: config.StorageProvider, SyncEnabled: true, LastSyncAt: now.Add(-5 * time.Second)}, "5s ago"},
	} {
		var out strings.Builder
		printStatus(&out, "http://x", api.StatusResponse{Time: now, Storage: tc.st})
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%+v: got %q, want %q", tc.st, out.String(), tc.want)
		}
	}
}

func TestClientAddr(t *testing.T) {
	for in, want := range map[string]string{"0.0.0.0:8080": "127.0.0.1:8080", "[::]:9": "127.0.0.1:9", "10.0.0.2:80": "10.0.0.2:80"} {
		if got := clientAddr(in); got != want {
			t.Errorf("clientAddr(%q) = %q", in, got)
		}
	}
}
