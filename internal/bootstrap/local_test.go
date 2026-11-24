package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/portwatcher/lydiabox/internal/config"
)

func buildManifest() *Manifest {
	return &Manifest{SchemaVersion: 1, Binaries: []Binary{
		{Name: "virtiofsd", Version: "1.14.0", RequiredFor: []string{"chv"}, Platforms: map[string]Artifact{
			"linux/arm64": {ChecksumSource: ChecksumBuild, Build: "make virtiofsd"}}},
	}}
}

func TestLocallyBuiltBinary(t *testing.T) {
	ctx := context.Background()
	m := buildManifest()
	b := newBoot(t, m, config.DriverCHV, "arm64", nil)

	// Missing: bootstrap explains how to build it instead of downloading.
	_, err := b.Ensure(ctx)
	if err == nil || !strings.Contains(err.Error(), "built from source") || !strings.Contains(err.Error(), "make virtiofsd") ||
		!strings.Contains(err.Error(), "dist/virtiofsd-linux-arm64") {
		t.Fatalf("missing built binary: %v", err)
	}

	// install-local copies the build output and records the version.
	src := filepath.Join(t.TempDir(), "virtiofsd-linux-arm64")
	if err := os.WriteFile(src, []byte("\x7fELF built"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest, err := b.InstallLocal("virtiofsd", src)
	if err != nil || dest != filepath.Join(b.opts.BinDir, "virtiofsd") {
		t.Fatalf("InstallLocal = %q, %v", dest, err)
	}
	if got := readBin(t, b, "virtiofsd"); got != "\x7fELF built" {
		t.Fatalf("installed content %q", got)
	}
	rec, _ := b.readRecord()
	if r := rec["virtiofsd"]; r.Version != "1.14.0" || r.URL != "build:make virtiofsd" || r.SHA256 != sum([]byte("\x7fELF built")) {
		t.Fatalf("install record %+v", r)
	}
	statuses, err := b.Ensure(ctx)
	if err != nil || statuses[0].Source != "runtime" || statuses[0].Outdated {
		t.Fatalf("Ensure with a built binary: %+v %v", statuses, err)
	}

	// A manifest upgrade marks it outdated, but Ensure keeps it (no download).
	m.Binaries[0].Version = "1.15.0"
	statuses, err = b.Ensure(ctx)
	if err != nil || !statuses[0].Outdated || readBin(t, b, "virtiofsd") != "\x7fELF built" {
		t.Fatalf("outdated built binary: %+v %v", statuses, err)
	}

	if _, err := b.InstallLocal("virtiofsd", filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing source must fail")
	}
	if _, err := b.InstallLocal("nope", src); err == nil {
		t.Fatal("an unknown binary must fail")
	}
}

func TestLocallyBuiltBinaryMirror(t *testing.T) {
	f := newFixture(t)
	u, s := f.add("/virtiofsd-arm64", []byte("mirror"))
	b := newBoot(t, buildManifest(), config.DriverCHV, "arm64", map[string]config.Mirror{"virtiofsd": {URL: u, SHA256: s}})
	if _, err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readBin(t, b, "virtiofsd"); got != "mirror" {
		t.Fatalf("mirror download = %q", got)
	}
}

func TestBuildEntryValidation(t *testing.T) {
	for name, doc := range map[string]string{
		"build with url":      `{"schemaVersion":1,"binaries":[{"name":"x","version":"1","platforms":{"linux/arm64":{"url":"https://x","checksumSource":"build","build":"make x","archive":"raw"}}}]}`,
		"build without build": `{"schemaVersion":1,"binaries":[{"name":"x","version":"1","platforms":{"linux/arm64":{"checksumSource":"build"}}}]}`,
	} {
		if _, err := ParseManifest([]byte(doc)); err == nil {
			t.Errorf("%s: must fail", name)
		}
	}
	m, err := DefaultManifest()
	if err != nil {
		t.Fatal(err)
	}
	vfs, _ := m.Lookup("virtiofsd")
	arm := vfs.Platforms["linux/arm64"]
	if !arm.BuiltLocally() || arm.Build != "make virtiofsd" || arm.URL != "" {
		t.Fatalf("virtiofsd linux/arm64 = %+v", arm)
	}
	if amd := vfs.Platforms["linux/amd64"]; amd.BuiltLocally() || amd.URL == "" || amd.SHA256 == "" {
		t.Fatalf("virtiofsd linux/amd64 = %+v", amd)
	}
}
