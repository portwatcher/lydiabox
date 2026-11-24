//go:build unix

package api_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/api"
)

// A cell listens on a UNIX socket and answers /api/v1/ready without a
// token, with its tenant ID.
func TestUnixListenerAndReady(t *testing.T) {
	dir, err := os.MkdirTemp("", "lbxa")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "cell.sock")
	// A socket file left by a killed cell is replaced.
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	s, err := api.NewServer(api.Options{Addr: "unix:" + sock, Auth: api.NewAuthenticator("tok-0123456789abcdef"), TenantID: "alice"},
		api.CoreRoutes{Auth: api.NewAuthenticator("tok-0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	defer s.Shutdown(context.Background())
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode %v %v", fi, err)
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr}).Get("http://cell/api/v1/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"tenantId":"alice"`) {
		t.Fatalf("ready %d %s", resp.StatusCode, b)
	}
	// A regular file at the path is not replaced.
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s2, _ := api.NewServer(api.Options{Addr: "unix:" + file, Auth: api.NewAuthenticator("tok-0123456789abcdef"), TenantID: "alice"})
	if _, err := s2.Listen(); err == nil {
		t.Fatal("a regular file was replaced by the socket")
	}
}
