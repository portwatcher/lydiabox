package cli

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
	ib "github.com/lydiabox/lydiabox/internal/instabox"
	"github.com/lydiabox/lydiabox/internal/instabox/instaboxtest"
	"github.com/lydiabox/lydiabox/internal/vmm"
	vmmib "github.com/lydiabox/lydiabox/internal/vmm/instabox"
)

// makeMachine makes the tenant's machine with the driver, as `lydiabox up`
// does on first use, and returns its ID.
func makeMachine(t *testing.T, fake *instaboxtest.Server) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStateStore(ctx, Env{Stdout: &bytes.Buffer{}}, cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client, err := newInstaboxClient(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	d, err := vmmib.New(vmmib.Options{Client: client, Store: st, Snapshot: cfg.Instabox.Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Create(ctx, cfg.TenantID, vmm.Spec{CPUs: 2, MemMB: 4096}); err != nil {
		t.Fatal(err)
	}
	rec, _, err := vmmib.LoadRecord(ctx, st, cfg.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	return rec.MachineID
}

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Main(context.Background(), args, Env{Stdout: &out, Stderr: &out})
	return code, out.String()
}

func TestInstaboxCommand(t *testing.T) {
	fake := instaboxtest.New(instaboxtest.Options{OpDelay: 5 * time.Millisecond})
	t.Cleanup(fake.Close)
	t.Chdir(t.TempDir())
	for k, v := range map[string]string{
		"LYDIABOX_DATA_DIR": t.TempDir(), "LYDIABOX_TENANT_ID": "alice", "LYDIABOX_STORAGE_MODE": "provider",
		"LYDIABOX_INSTABOX_API_URL": fake.URL, "LYDIABOX_INSTABOX_API_KEY": fake.APIKey(),
		"LYDIABOX_INSTABOX_SNAPSHOT": fake.AddSnapshot("img"), "LYDIABOX_AUTH_TOKEN": "admin-token-0123456789abcdef",
	} {
		t.Setenv(k, v)
	}
	if code, out := runCLI(t, "instabox", "status"); code != 0 || !strings.Contains(out, "Machine:  none") {
		t.Fatalf("status without a machine: %d %s", code, out)
	}
	id := makeMachine(t, fake)
	code, out := runCLI(t, "instabox", "status")
	if code != 0 || !strings.Contains(out, id) || !strings.Contains(out, "State:") || strings.Contains(out, fake.APIKey()) {
		t.Fatalf("status: %d %s", code, out)
	}
	// Without the exact machine ID nothing is deleted.
	for _, args := range [][]string{{"instabox", "destroy"}, {"instabox", "destroy", "-confirm", "m_other"}, {"instabox", "destroy", "-forget"}} {
		if code, out := runCLI(t, args...); code == 0 || fake.State(id) == "" {
			t.Fatalf("%v deleted the machine: %d %s", args, code, out)
		}
	}
	if code, out := runCLI(t, "instabox", "destroy", "-confirm", id); code != 0 || !strings.Contains(out, "Deleted machine "+id) {
		t.Fatalf("destroy: %d %s", code, out)
	}
	if fake.State(id) != "" {
		t.Fatalf("machine state after destroy %q", fake.State(id))
	}
	if code, out := runCLI(t, "instabox", "status"); code != 0 || !strings.Contains(out, "Machine:  none") {
		t.Fatalf("status after destroy: %d %s", code, out)
	}

	// A machine deleted outside LydiaBox: status says so, and -forget
	// removes the record.
	id = makeMachine(t, fake)
	op, err := mustClient(t, fake).DeleteMachine(context.Background(), id)
	if err == nil {
		_, err = mustClient(t, fake).WaitOperation(context.Background(), op)
	}
	if err != nil {
		t.Fatal(err)
	}
	if code, out := runCLI(t, "instabox", "status"); code != 0 || !strings.Contains(out, "does not exist") {
		t.Fatalf("status of a deleted machine: %d %s", code, out)
	}
	if code, out := runCLI(t, "instabox", "destroy", "-forget"); code != 0 || !strings.Contains(out, "Removed the record") {
		t.Fatalf("forget: %d %s", code, out)
	}
}

func mustClient(t *testing.T, fake *instaboxtest.Server) *ib.Client {
	t.Helper()
	cfg := &config.Config{Instabox: config.InstaboxConfig{APIURL: fake.URL, APIKey: fake.APIKey()}}
	c, err := newInstaboxClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
