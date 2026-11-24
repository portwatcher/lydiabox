package cli

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/cloudflare/cloudflaretest"
	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/store"
)

// cloudflareCLI isolates the CLI and points it at a fake Cloudflare API.
func cloudflareCLI(t *testing.T) *cloudflaretest.API {
	t.Helper()
	isolate(t)
	f := cloudflaretest.New(t)
	old := cloudflareAPIURL
	cloudflareAPIURL = f.URL()
	t.Cleanup(func() { cloudflareAPIURL = old })
	t.Setenv("LYDIABOX_CLOUDFLARE_API_TOKEN", cloudflaretest.Token)
	t.Setenv("LYDIABOX_CLOUDFLARE_ACCOUNT_ID", cloudflaretest.Account)
	for _, k := range []string{"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_ACCOUNT_ID", "LYDIABOX_LISTEN_ADDR", "LYDIABOX_ACCESS_EMAILS"} {
		t.Setenv(k, "")
	}
	return f
}

func storedSetup(t *testing.T) (bool, string) {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSecrets(); err != nil {
		t.Fatal(err)
	}
	key, _ := cfg.MasterKeyBytes()
	st, err := store.Open(context.Background(), cfg.DBPath(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	state, sec, ok, err := loadCloudflare(context.Background(), st, cfg.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	return ok, state.Hostname + " " + state.OriginURL + " " + sec.TunnelToken
}

func TestCloudflareSetupStatusTeardown(t *testing.T) {
	f := cloudflareCLI(t)
	code, out, errOut := run(t, "cloudflare", "setup", "-hostname", "agent.example.com", "-email", "me@example.com",
		"-bypass", "/webdav", "-install=false")
	if code != 0 {
		t.Fatalf("setup: %d %s %s", code, out, errOut)
	}
	if !strings.Contains(out, "created tunnel") || !strings.Contains(out, "https://agent.example.com") ||
		!strings.Contains(out, "CF-Access-Client-Secret: secret-") || !strings.Contains(out, "skips the Access login") ||
		!strings.Contains(out, "created TURN key") || !strings.Contains(out, "through Cloudflare TURN (relay only") {
		t.Fatalf("setup output:\n%s", out)
	}
	if !strings.Contains(out, "Generated admin token") {
		t.Fatalf("a first run must print the new admin token:\n%s", out)
	}
	ok, summary := storedSetup(t)
	if !ok || !strings.HasPrefix(summary, "agent.example.com http://127.0.0.1:8080 token-") {
		t.Fatalf("stored = %v %q", ok, summary)
	}
	// The tunnel token lives only encrypted in the state database.
	cfg, _ := config.Load(config.LoadOptions{})
	found := false
	for _, p := range []string{cfg.DBPath(), cfg.DBPath() + "-wal"} {
		raw, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "token-tun") || strings.Contains(string(raw), "secret-svc") {
			t.Fatalf("%s holds a secret in plain text", p)
		}
		if strings.Contains(string(raw), "agent.example.com") {
			found = true
		}
	}
	if !found {
		t.Fatal("the state database does not hold the setup")
	}

	// A second run converges and keeps the service token secret.
	code, out, errOut = run(t, "cloudflare", "setup", "-hostname", "agent.example.com", "-email", "me@example.com",
		"-bypass", "/webdav", "-install=false")
	if code != 0 || !strings.Contains(out, "reused tunnel") || !strings.Contains(out, "Secret: unchanged") {
		t.Fatalf("second setup: %d\n%s\n%s", code, out, errOut)
	}
	if len(f.Tunnels) != 1 || len(f.Apps) != 2 || len(f.TURNKeys) != 1 || !strings.Contains(out, "reused TURN key") {
		t.Fatalf("duplicates: %d tunnels, %d apps", len(f.Tunnels), len(f.Apps))
	}

	code, out, _ = run(t, "cloudflare", "status")
	if code != 0 || !strings.Contains(out, "https://agent.example.com") || !strings.Contains(out, "team.cloudflareaccess.com") ||
		strings.Contains(out, "secret-") {
		t.Fatalf("status: %d\n%s", code, out)
	}
	if !strings.Contains(out, "TURN key") {
		t.Fatalf("status without the TURN key:\n%s", out)
	}
	if _, out, _ = run(t, "cloudflare", "status", "-show-secret"); !strings.Contains(out, "secret-") || !strings.Contains(out, "token-tun") ||
		!strings.Contains(out, "TURN key secret") {
		t.Fatalf("status -show-secret:\n%s", out)
	}

	code, out, errOut = run(t, "cloudflare", "teardown")
	if code != 0 || !strings.Contains(out, "Remote access is removed") {
		t.Fatalf("teardown: %d\n%s\n%s", code, out, errOut)
	}
	if len(f.Tunnels) != 0 || len(f.Apps) != 0 || len(f.Records) != 0 || len(f.TURNKeys) != 0 {
		t.Fatal("teardown left resources")
	}
	if ok, _ := storedSetup(t); ok {
		t.Fatal("teardown must delete the stored setup")
	}
	if _, out, _ = run(t, "cloudflare", "status"); !strings.Contains(out, "not set up") {
		t.Fatalf("status after teardown:\n%s", out)
	}
	if code, _, errOut = run(t, "cloudflare", "teardown"); code != 1 || !strings.Contains(errOut, "-hostname") {
		t.Fatalf("teardown without setup: %d %s", code, errOut)
	}
}

func TestCloudflareSetupFailureStoresNothing(t *testing.T) {
	f := cloudflareCLI(t)
	f.Fail["POST /accounts/acct1/access/apps"] = http.StatusInternalServerError
	code, _, errOut := run(t, "cloudflare", "setup", "-hostname", "agent.example.com", "-email", "me@example.com", "-install=false")
	if code != 1 || !strings.Contains(errOut, "HTTP 500") {
		t.Fatalf("setup: %d %s", code, errOut)
	}
	if ok, _ := storedSetup(t); ok {
		t.Fatal("a failed setup must store nothing")
	}
	// The next run reuses what the failed run created.
	if code, out, errOut := run(t, "cloudflare", "setup", "-hostname", "agent.example.com", "-email", "me@example.com", "-install=false"); code != 0 ||
		!strings.Contains(out, "reused tunnel") {
		t.Fatalf("retry: %d\n%s\n%s", code, out, errOut)
	}
}

func TestCloudflareTeardownFailureKeepsSetup(t *testing.T) {
	f := cloudflareCLI(t)
	if code, _, errOut := run(t, "cloudflare", "setup", "-hostname", "agent.example.com", "-email", "me@example.com", "-install=false"); code != 0 {
		t.Fatal(errOut)
	}
	f.Fail["DELETE /accounts/acct1/cfd_tunnel/"] = http.StatusBadRequest
	if code, _, errOut := run(t, "cloudflare", "teardown"); code != 1 || !strings.Contains(errOut, "kept") {
		t.Fatalf("teardown: %d %s", code, errOut)
	}
	if ok, _ := storedSetup(t); !ok {
		t.Fatal("a failed teardown must keep the stored setup")
	}
}

func TestCloudflareUsageAndCredentials(t *testing.T) {
	cloudflareCLI(t)
	if code, _, errOut := run(t, "cloudflare"); code != 2 || !strings.Contains(errOut, "teardown") {
		t.Fatalf("usage: %d %s", code, errOut)
	}
	if code, _, _ := run(t, "cloudflare", "frob"); code != 2 {
		t.Fatalf("unknown subcommand: %d", code)
	}
	if code, _, _ := run(t, "cloudflare", "setup", "-hostname", "agent.example.com"); code != 2 {
		t.Fatalf("missing email: %d", code)
	}
	t.Setenv("LYDIABOX_CLOUDFLARE_API_TOKEN", "")
	code, _, errOut := run(t, "cloudflare", "setup", "-hostname", "agent.example.com", "-email", "me@example.com")
	if code != 1 || !strings.Contains(errOut, "LYDIABOX_CLOUDFLARE_API_TOKEN") {
		t.Fatalf("missing token: %d %s", code, errOut)
	}
	if code, _, errOut := run(t, "cloudflare", "setup", "-hostname", "bad host", "-email", "me@example.com"); code != 1 {
		t.Fatalf("bad hostname: %d %s", code, errOut)
	}
}

func TestDefaultOrigin(t *testing.T) {
	for _, tc := range []struct {
		listen string
		set    bool
		want   string
	}{
		{"0.0.0.0:8080", false, "http://127.0.0.1:8080"},
		{"0.0.0.0:9000", true, "http://127.0.0.1:9000"},
		{"10.0.0.5:8080", true, "http://10.0.0.5:8080"},
		{"[::1]:8080", true, "http://[::1]:8080"},
		{"localhost:8080", true, "http://127.0.0.1:8080"},
	} {
		cfg := config.Defaults(t.TempDir())
		cfg.ListenAddr, cfg.Remote.ListenAddrSet = tc.listen, tc.set
		if got := defaultOrigin(cfg); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.listen, got, tc.want)
		}
	}
	if !originMatches("http://127.0.0.1:8080", "0.0.0.0:8080") || originMatches("http://127.0.0.1:8080", "127.0.0.1:9000") {
		t.Fatal("originMatches")
	}
}
