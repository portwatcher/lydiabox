package storage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/lydiabox/lydiabox/internal/config"
)

func TestStateBackupWithoutS3(t *testing.T) {
	ctx := context.Background()
	cfg := config.Defaults(t.TempDir())
	cfg.DataDir, cfg.Storage.Mode = t.TempDir(), config.StorageProvider
	var logs bytes.Buffer
	b, err := NewStateBackup(StateBackupOptions{Config: cfg, State: &fakeState{content: "db"},
		Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 || !strings.Contains(logs.String(), "not backed up") {
		t.Fatalf("want one warning, got %q", logs.String())
	}
	if err := b.SyncNow(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := b.Stop(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.HomeDir("default"); !errors.Is(err, ErrNoHostDirs) {
		t.Fatalf("HomeDir = %v", err)
	}
	if st := b.Status("default"); st.Mode != config.StorageProvider || st.Mounted {
		t.Fatalf("status = %+v", st)
	}
	if err := b.Start(ctx, "other"); !errors.Is(err, ErrUnknownTenant) {
		t.Fatalf("other tenant = %v", err)
	}
}

func TestStateBackupUploadsState(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.Storage.Mode = config.StorageProvider
	objs := newMemObjects()
	state := &fakeState{content: "state v1"}
	b, err := NewStateBackup(StateBackupOptions{Config: cfg, Objects: objs, State: state, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncNow(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if err := b.SyncNow(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if objs.putCount(StateBackupKey("default")) != 1 {
		t.Fatal("an unchanged snapshot must not be uploaded again")
	}
	if objs.putCount(MetaBackupKey("default")) != 0 {
		t.Fatal("the provider mode has no JuiceFS metadata to upload")
	}
	state.mu.Lock()
	state.content = "state v2"
	state.mu.Unlock()
	if err := b.Stop(ctx, "default"); err != nil {
		t.Fatal(err)
	}
	if objs.putCount(StateBackupKey("default")) != 2 {
		t.Fatal("Stop must upload the final snapshot")
	}
	if st := b.Status("default"); st.LastUploadAt.IsZero() || st.LastSyncError != "" {
		t.Fatalf("status = %+v", st)
	}
}
