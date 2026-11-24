package cli

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/agentfs"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/storage"
	"github.com/portwatcher/lydiabox/internal/store"
)

type stubStorage struct{ dir string }

func (stubStorage) Start(context.Context, string) error   { return nil }
func (stubStorage) Stop(context.Context, string) error    { return nil }
func (stubStorage) Status(tid string) storage.Status      { return storage.Status{TenantID: tid} }
func (stubStorage) SyncNow(context.Context, string) error { return nil }
func (s stubStorage) HomeDir(string) (string, error)      { return s.dir, nil }
func (s stubStorage) WorkspaceDir(string) (string, error) { return s.dir, nil }

func TestWireConnectorServesBrokerSocket(t *testing.T) {
	ctx := context.Background()
	dataDir, err := os.MkdirTemp("", "lbx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	cfg := config.Defaults(dataDir)
	cfg.DataDir = dataDir
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d := &deps{Config: cfg, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: st, Storage: stubStorage{t.TempDir()}}
	d.Files = agentfs.NewLocal(d.Storage, agentfs.LocalOptions{})
	c, err := wireConnector(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "connector" || len(c.Routes) != 1 || connectorBroker(d) == nil || connectorBroker(d).TenantID() != "default" {
		t.Fatalf("component = %+v", c)
	}
	// A stale socket from a crashed run is replaced.
	sock := cfg.HostBrokerSocket()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := stale.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	stale.Close()

	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	resp, err := client.Post("http://host/git-cred", "application/json", strings.NewReader(`{"protocol":"https","host":"github.com","path":""}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("git-cred without accounts: %d", resp.StatusCode)
	}
	client.CloseIdleConnections()
	if err := c.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
	if connectorBroker(d) != nil {
		t.Fatal("broker still registered after Stop")
	}

	// A regular file at the socket path is never deleted.
	if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenUnix(sock); err == nil {
		t.Fatal("listenUnix replaced a regular file")
	}
}
