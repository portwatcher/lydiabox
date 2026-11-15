package gitcred_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"github.com/lydiabox/lydiabox/internal/agentfs"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/connector"
	"github.com/lydiabox/lydiabox/internal/connector/broker"
	"github.com/lydiabox/lydiabox/internal/connector/connectortest"
	"github.com/lydiabox/lydiabox/internal/guest/cli"
	"github.com/lydiabox/lydiabox/internal/guest/gitcred"
	"github.com/lydiabox/lydiabox/internal/store"
)

type dirs struct{ dir string }

func (d dirs) HomeDir(string) (string, error)      { return d.dir, nil }
func (d dirs) WorkspaceDir(string) (string, error) { return d.dir, nil }

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// runGuest runs lydiabox-guest in-process the way git invokes the helper.
func runGuest(t *testing.T, argv0 string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Main(context.Background(), argv0, args, cli.Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb})
	return code, out.String(), errb.String()
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != root {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// TestGitCredEndToEnd is the Phase 4 verification gate: the guest helper
// fetches a token over the broker socket (the docker driver's transport and
// the stand-in for vsock), the token comes from the encrypted host store
// (refreshed on expiry), and the guest writes nothing to disk.
func TestGitCredEndToEnd(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	hostDir := t.TempDir()
	dbPath := filepath.Join(hostDir, "state.db")
	st, err := store.Open(ctx, dbPath, key)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fake := connectortest.New(t)
	clk := &clock{t: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr, err := connector.New(connector.Options{Store: st, Providers: fake.Providers(), Now: clk.Now, Logger: log,
		EnvClients: map[string]connector.OAuthClient{connector.GitHub: {ClientID: connectortest.ClientID, ClientSecret: connectortest.ClientSecret}}})
	if err != nil {
		t.Fatal(err)
	}

	// Connect GitHub through the OAuth PKCE flow.
	res, err := mgr.Authorize(ctx, "default", connector.GitHub, connector.AuthorizeRequest{RedirectURI: "http://lydia.test/api/v1/connectors/github/callback"})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := fake.Approve(res.AuthorizeURL, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	cbu, _ := url.Parse(cb)
	if _, err := mgr.Callback(ctx, "default", connector.GitHub, connector.CallbackRequest{Code: cbu.Query().Get("code"), State: res.State}); err != nil {
		t.Fatal(err)
	}

	brk, err := broker.New(broker.Options{Manager: mgr, TenantID: "default", Files: agentfs.NewLocal(dirs{hostDir}, agentfs.LocalOptions{}), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("", "lbx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "host-9001.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- brk.Serve(sctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	// The guest process: a private HOME and TMPDIR that must stay empty.
	guestHome, guestTmp := t.TempDir(), t.TempDir()
	t.Setenv("HOME", guestHome)
	t.Setenv("TMPDIR", guestTmp)
	t.Setenv(gitcred.HostAddrEnv, "unix://"+sock)
	input := "protocol=https\nhost=github.com\npath=octo/hello.git\n\n"

	code, out, errOut := runGuest(t, "/usr/local/bin/lydiabox-guest", input, "git-cred", "get")
	stored, err := st.GetConnectorAccount(ctx, "default", connector.GitHub, "octocat")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || out != "username=x-access-token\npassword="+stored.AccessToken+"\n" {
		t.Fatalf("git-cred get: code %d out %q err %q", code, out, errOut)
	}

	// After expiry, the broker refreshes with the encrypted refresh token.
	clk.Add(2 * time.Hour)
	code, out, errOut = runGuest(t, "/usr/local/bin/git-credential-lydiabox", input, "get")
	refreshed, _ := st.GetConnectorAccount(ctx, "default", connector.GitHub, "octocat")
	if code != 0 || refreshed.AccessToken == stored.AccessToken || out != "username=x-access-token\npassword="+refreshed.AccessToken+"\n" {
		t.Fatalf("refreshed get: code %d out %q err %q", code, out, errOut)
	}
	if fake.RefreshCount() != 1 {
		t.Fatalf("refreshes = %d", fake.RefreshCount())
	}

	// Hosts without a connected account produce no credential.
	if code, out, _ := runGuest(t, "lydiabox-guest", "protocol=https\nhost=gitlab.com\n\n", "git-cred", "get"); code != 0 || out != "" {
		t.Fatalf("unknown host: %d %q", code, out)
	}
	for _, action := range []string{"store", "erase"} {
		if code, out, _ := runGuest(t, "lydiabox-guest", input+"password=whatever\n", "git-cred", action); code != 0 || out != "" {
			t.Fatalf("%s: %d %q", action, code, out)
		}
	}

	if files := append(listFiles(t, guestHome), listFiles(t, guestTmp)...); len(files) != 0 {
		t.Fatalf("the guest wrote files: %v", files)
	}

	// Tokens are encrypted at rest in the host database.
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(dbPath + suffix)
		if err != nil && suffix == "" {
			t.Fatal(err)
		}
		for _, secret := range []string{stored.AccessToken, refreshed.AccessToken, refreshed.RefreshToken} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("%s contains a plaintext token", filepath.Base(dbPath+suffix))
			}
		}
	}

	page, err := mgr.Audit(ctx, "default", connector.GitHub, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Entries {
		kinds = append(kinds, e.Kind)
	}
	want := "connector.token.issued,connector.token.refreshed,connector.token.issued,connector.connected"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("audit = %v", kinds)
	}
}
