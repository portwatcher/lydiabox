package instabox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/instabox"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
)

// TestMain lets the test binary act as the instabox CLI for
// ImportSnapshot: with LYDIABOX_FAKE_INSTABOX_CLI set it imports the way
// the real CLI does (a throwaway machine, a snapshot, a delete) against
// the fake server named by INSTABOX_API_URL.
func TestMain(m *testing.M) {
	if mode := os.Getenv("LYDIABOX_FAKE_INSTABOX_CLI"); mode != "" {
		os.Exit(fakeCLI(mode, os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeCLI(mode string, args []string) int {
	record := map[string]any{"args": args, "password": os.Getenv("INSTABOX_REGISTRY_PASSWORD"),
		"stray": os.Getenv("INSTABOX_STRAY")}
	data, _ := json.Marshal(record)
	_ = os.WriteFile(os.Getenv("LYDIABOX_FAKE_CLI_RECORD"), data, 0o600)
	fmt.Fprintln(os.Stderr, "pulling image")
	if mode == "fail" {
		fmt.Println(`{"error":{"code":"quota_exceeded","message":"snapshot limit reached","requestId":"r","retryable":false,"operationId":null}}`)
		return 5
	}
	if len(args) < 5 || args[0] != "snapshot" || args[1] != "import" || args[3] != "--name" {
		fmt.Fprintln(os.Stderr, "usage")
		return 2
	}
	c, err := instabox.NewClient(instabox.Options{APIKey: os.Getenv("INSTABOX_API_KEY"), BaseURL: os.Getenv("INSTABOX_API_URL")})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	op, err := c.CreateMachine(ctx, instabox.CreateMachineRequest{Name: "builder", TemplateID: "agent-browser"})
	if err == nil {
		_, err = c.WaitOperation(ctx, op)
	}
	var snap *instabox.Snapshot
	if err == nil {
		snap, err = c.CreateSnapshot(ctx, op.MachineID, instabox.CreateSnapshotRequest{Name: args[4]})
	}
	if err == nil {
		snap, err = c.WaitSnapshot(ctx, snap.ID)
	}
	if err == nil {
		_, err = c.DeleteMachine(ctx, op.MachineID)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	out, _ := json.Marshal(map[string]string{"id": snap.ID, "name": snap.Name})
	fmt.Println(string(out))
	return 0
}

func TestImportSnapshot(t *testing.T) {
	r := newRig(t, instaboxtest.Options{OpDelay: time.Millisecond})
	record := t.TempDir() + "/record.json"
	t.Setenv("LYDIABOX_FAKE_INSTABOX_CLI", "ok")
	t.Setenv("LYDIABOX_FAKE_CLI_RECORD", record)
	t.Setenv("INSTABOX_STRAY", "from-parent")
	var progress strings.Builder
	snap, err := r.client.ImportSnapshot(ctxT(t), instabox.ImportOptions{
		Image: "ghcr.io/lydiabox/instabox:test", Name: "lydiabox-test", CLI: os.Args[0],
		RegistryUsername: "bot", RegistryPassword: "registry-secret", Timeout: 20 * time.Minute, Progress: &progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Name != "lydiabox-test" || snap.Status != instabox.SnapshotReady || !strings.Contains(progress.String(), "pulling") {
		t.Fatalf("snapshot %+v, progress %q", snap, progress.String())
	}
	var rec struct {
		Args     []string `json:"args"`
		Password string   `json:"password"`
		Stray    string   `json:"stray"`
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.Args, " ")
	want := "snapshot import ghcr.io/lydiabox/instabox:test --name lydiabox-test --json --registry-username bot --timeout 20m0s"
	if joined != want || rec.Password != "registry-secret" || rec.Stray != "" {
		t.Fatalf("CLI got args %q password %q stray %q", joined, rec.Password, rec.Stray)
	}
	if strings.Contains(joined, "registry-secret") || strings.Contains(joined, r.fake.APIKey()) {
		t.Fatal("a secret is on the command line")
	}
}

func TestImportSnapshotErrors(t *testing.T) {
	r := newRig(t, instaboxtest.Options{})
	ctx := ctxT(t)
	if _, err := r.client.ImportSnapshot(ctx, instabox.ImportOptions{Name: "x"}); err == nil {
		t.Fatal("no image accepted")
	}
	if _, err := r.client.ImportSnapshot(ctx, instabox.ImportOptions{Image: "x", Name: "Bad_Name"}); err == nil {
		t.Fatal("bad name accepted")
	}
	t.Setenv("LYDIABOX_FAKE_INSTABOX_CLI", "fail")
	t.Setenv("LYDIABOX_FAKE_CLI_RECORD", t.TempDir()+"/r.json")
	_, err := r.client.ImportSnapshot(ctx, instabox.ImportOptions{Image: "x:1", Name: "x", CLI: os.Args[0]})
	if !errors.Is(err, instabox.ErrQuota) && instabox.CodeOf(err) != instabox.CodeQuotaExceeded {
		t.Fatalf("CLI error: %v", err)
	}
	_, err = r.client.ImportSnapshot(ctx, instabox.ImportOptions{Image: "x:1", Name: "x", CLI: "/nonexistent/instabox"})
	if err == nil {
		t.Fatal("missing CLI accepted")
	}
}

func TestImportArgs(t *testing.T) {
	got := strings.Join(instabox.ImportArgs(instabox.ImportOptions{Image: "img", Name: "n", NoVerify: true, KeepBuilder: true}), " ")
	if got != "snapshot import img --name n --json --no-verify --keep-builder" {
		t.Fatal(got)
	}
}
