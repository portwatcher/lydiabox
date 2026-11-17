package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lydiabox/lydiabox/internal/agentfs"
	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/guest/rpc"
	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
	"github.com/lydiabox/lydiabox/internal/harness/adapters/adapterstest"
	"github.com/lydiabox/lydiabox/internal/harness/harnesstest"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestSessionOfAHarnessThatIsNotInstalled: the guest does not find the
// program of the harness. The answer is 409 not_installed with a text that
// names the harness, and the host logs it at info level, not as an error.
func TestSessionOfAHarnessThatIsNotInstalled(t *testing.T) {
	home := adapterstest.Home(t)
	ws := filepath.Join(filepath.Dir(filepath.Dir(home)), "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	var logs lockedBuffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runner := &harnesstest.Runner{Err: &rpc.Error{Code: rpc.CodeBadRequest, Message: `command "claude" not found`}}
	mgr, err := harness.NewManager(harness.ManagerOptions{Adapters: adapters.All(),
		Files:     agentfs.NewLocal(harnesstest.Dirs{Home: home, Workspace: ws}, agentfs.LocalOptions{}),
		Discovery: harness.LocalDiscovery{Dirs: harnesstest.Dirs{Home: home, Workspace: ws}, Adapters: adapters.All()},
		Store:     &harnesstest.Store{}, Runner: runner, Waker: &harnesstest.Waker{}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	s, err := api.NewServer(api.Options{Addr: "127.0.0.1:0", Auth: api.NewAuthenticator(token), TenantID: "default", Logger: log},
		api.SessionsRoutes{Manager: mgr})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sessions", strings.NewReader(`{"harness":"claude-code","workspaceDir":"/workspace"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	want := `Claude Code is not installed in the VM (the command "claude" was not found)`
	if resp.StatusCode != http.StatusConflict || body.Error.Code != api.CodeNotInstalled || body.Error.Message != want {
		t.Fatalf("answer = %d %+v, want 409 %s %q", resp.StatusCode, body.Error, api.CodeNotInstalled, want)
	}
	out := logs.String()
	if !strings.Contains(out, `level=INFO msg="harness not installed in the VM" component=harness harness=claude-code command=claude`) {
		t.Fatalf("no info line:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") || strings.Contains(out, "level=WARN") {
		t.Fatalf("logged as a failure:\n%s", out)
	}
}
