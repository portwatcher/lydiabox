package bootstrap

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lydiabox/lydiabox/internal/config"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

type fixture struct {
	srv   *httptest.Server
	hits  atomic.Int64
	files map[string][]byte
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		body, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) add(path string, body []byte) (string, string) {
	f.files[path] = body
	return f.srv.URL + path, sum(body)
}

func testManifest(f *fixture, t *testing.T) *Manifest {
	jfsURL, jfsSum := f.add("/juicefs.tar.gz", tarGz(t, map[string]string{"LICENSE": "x", "juicefs": "#!juicefs-v2"}))
	chURL, chSum := f.add("/cloud-hypervisor", []byte("#!ch"))
	vfsURL, vfsSum := f.add("/virtiofsd.zip", zipArchive(t, map[string]string{"target/release/virtiofsd": "#!vfs"}))
	return &Manifest{SchemaVersion: 1, Binaries: []Binary{
		{Name: "juicefs", Version: "2", RequiredFor: []string{"chv", "docker"}, Platforms: map[string]Artifact{
			"linux/amd64": {URL: jfsURL, SHA256: jfsSum, Archive: ArchiveTarGz, Member: "juicefs"}}},
		{Name: "cloud-hypervisor", Version: "54", RequiredFor: []string{"chv"}, Platforms: map[string]Artifact{
			"linux/amd64": {URL: chURL, SHA256: chSum, Archive: ArchiveRaw}}},
		{Name: "virtiofsd", Version: "1.14", RequiredFor: []string{"chv"}, Platforms: map[string]Artifact{
			"linux/amd64": {URL: vfsURL, SHA256: vfsSum, Archive: ArchiveZip, Member: "target/release/virtiofsd"},
			"linux/arm64": {ChecksumSource: ChecksumUnverified}}},
		{Name: "magpie", Version: "0.1", RequiredFor: []string{"chv", "docker"}, Platforms: map[string]Artifact{
			"linux/amd64": {URL: f.srv.URL + "/magpie", SHA256: "", ChecksumSource: ChecksumUnverified, Archive: ArchiveRaw}}},
	}}
}

func newBoot(t *testing.T, m *Manifest, driver, arch string, mirrors map[string]config.Mirror) *Bootstrapper {
	t.Helper()
	b, err := New(Options{
		BinDir: filepath.Join(t.TempDir(), "bin"), Driver: driver, GOOS: "linux", GOARCH: arch,
		Manifest: m, Mirrors: mirrors, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		LookPath: func(string) (string, error) { return "", errors.New("not on PATH") },
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readBin(t *testing.T, b *Bootstrapper, name string) string {
	t.Helper()
	p := filepath.Join(b.opts.BinDir, name)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s is not executable: %v", name, fi.Mode())
	}
	data, _ := os.ReadFile(p)
	return string(data)
}

func TestEnsureInstallsRequiredBinaries(t *testing.T) {
	f := newFixture(t)
	m := testManifest(f, t)
	// magpie has no pinned checksum: Ensure must refuse it but install the rest.
	b := newBoot(t, m, config.DriverCHV, "amd64", nil)
	statuses, err := b.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "magpie") || !strings.Contains(err.Error(), "no pinned SHA256") {
		t.Fatalf("want magpie checksum refusal, got %v", err)
	}
	if got := readBin(t, b, "juicefs"); got != "#!juicefs-v2" {
		t.Fatalf("juicefs = %q", got)
	}
	if got := readBin(t, b, "cloud-hypervisor"); got != "#!ch" {
		t.Fatalf("cloud-hypervisor = %q", got)
	}
	if got := readBin(t, b, "virtiofsd"); got != "#!vfs" {
		t.Fatalf("virtiofsd = %q", got)
	}
	if _, err := os.Stat(filepath.Join(b.opts.BinDir, "magpie")); !os.IsNotExist(err) {
		t.Fatal("unverified binary must not be installed")
	}
	sources := map[string]string{}
	for _, s := range statuses {
		sources[s.Name] = s.Source
	}
	if sources["juicefs"] != "runtime" || sources["magpie"] != "missing" {
		t.Fatalf("statuses = %+v", statuses)
	}
	entries, _ := os.ReadDir(b.opts.BinDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".download-") || strings.HasPrefix(e.Name(), ".install-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}

	// A second Ensure downloads nothing for already installed binaries.
	before := f.hits.Load()
	b.opts.Mirrors = map[string]config.Mirror{"magpie": {SHA256: strings.Repeat("0", 64)}}
	f.files["/magpie"] = []byte("#!magpie")
	_, err = b.Ensure(context.Background())
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want checksum mismatch for magpie, got %v", err)
	}
	if f.hits.Load()-before != 1 {
		t.Fatalf("expected only the magpie download, got %d requests", f.hits.Load()-before)
	}
	b.opts.Mirrors = map[string]config.Mirror{"magpie": {SHA256: sum([]byte("#!magpie"))}}
	if _, err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readBin(t, b, "magpie"); got != "#!magpie" {
		t.Fatalf("magpie = %q", got)
	}
}

func TestDockerDriverSkipsVMMBinaries(t *testing.T) {
	f := newFixture(t)
	m := testManifest(f, t)
	b := newBoot(t, m, config.DriverDocker, "amd64", map[string]config.Mirror{"magpie": {SHA256: sum([]byte("m"))}})
	f.files["/magpie"] = []byte("m")
	if _, err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(b.opts.BinDir, "cloud-hypervisor")); !os.IsNotExist(err) {
		t.Fatal("docker driver must not download cloud-hypervisor")
	}
}

func TestChecksumMismatchKeepsExistingBinary(t *testing.T) {
	f := newFixture(t)
	m := testManifest(f, t)
	b := newBoot(t, m, config.DriverCHV, "amd64", nil)
	if err := b.Install(context.Background(), "cloud-hypervisor"); err != nil {
		t.Fatal(err)
	}
	f.files["/cloud-hypervisor"] = []byte("#!tampered")
	err := b.Install(context.Background(), "cloud-hypervisor")
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	if got := readBin(t, b, "cloud-hypervisor"); got != "#!ch" {
		t.Fatalf("tampered download replaced the binary: %q", got)
	}
}

func TestMirrorOverride(t *testing.T) {
	f := newFixture(t)
	m := testManifest(f, t)
	mirrorURL, mirrorSum := f.add("/mirror/ch-custom", []byte("#!mirror-ch"))
	b := newBoot(t, m, config.DriverCHV, "amd64", map[string]config.Mirror{
		"cloud-hypervisor": {URL: mirrorURL, SHA256: mirrorSum},
	})
	if err := b.Install(context.Background(), "cloud-hypervisor"); err != nil {
		t.Fatal(err)
	}
	if got := readBin(t, b, "cloud-hypervisor"); got != "#!mirror-ch" {
		t.Fatalf("got %q", got)
	}

	// A mirror without a checksum override is still verified against the pin.
	b2 := newBoot(t, m, config.DriverCHV, "amd64", map[string]config.Mirror{"cloud-hypervisor": {URL: mirrorURL}})
	if err := b2.Install(context.Background(), "cloud-hypervisor"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}

	// file:// mirrors serve air-gapped hosts.
	local := filepath.Join(t.TempDir(), "ch")
	os.WriteFile(local, []byte("#!local"), 0o644)
	b3 := newBoot(t, m, config.DriverCHV, "amd64", map[string]config.Mirror{
		"cloud-hypervisor": {URL: "file://" + local, SHA256: sum([]byte("#!local"))},
	})
	if err := b3.Install(context.Background(), "cloud-hypervisor"); err != nil {
		t.Fatal(err)
	}
}

func TestUnavailablePlatform(t *testing.T) {
	f := newFixture(t)
	b := newBoot(t, testManifest(f, t), config.DriverCHV, "arm64", nil)
	err := b.Install(context.Background(), "virtiofsd")
	if err == nil || !strings.Contains(err.Error(), "LYDIABOX_MIRROR_VIRTIOFSD") {
		t.Fatalf("want mirror hint, got %v", err)
	}
	if err := b.Install(context.Background(), "nope"); err == nil {
		t.Fatal("unknown binary must fail")
	}
}

func TestDownloadErrors(t *testing.T) {
	f := newFixture(t)
	m := testManifest(f, t)
	b := newBoot(t, m, config.DriverCHV, "amd64", map[string]config.Mirror{
		"cloud-hypervisor": {URL: f.srv.URL + "/404", SHA256: strings.Repeat("a", 64)},
	})
	if err := b.Install(context.Background(), "cloud-hypervisor"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want 404 error, got %v", err)
	}
	big := bytes.Repeat([]byte("x"), 2048)
	u, s := f.add("/big", big)
	b2 := newBoot(t, m, config.DriverCHV, "amd64", map[string]config.Mirror{"cloud-hypervisor": {URL: u, SHA256: s}})
	b2.opts.MaxDownloadBytes = 1024
	if err := b2.Install(context.Background(), "cloud-hypervisor"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("want size limit error, got %v", err)
	}
	// Archive without the member.
	u, s = f.add("/empty.tar.gz", tarGz(t, map[string]string{"README": "x"}))
	b3 := newBoot(t, m, config.DriverCHV, "amd64", map[string]config.Mirror{"juicefs": {URL: u, SHA256: s}})
	if err := b3.Install(context.Background(), "juicefs"); err == nil || !strings.Contains(err.Error(), "no regular file") {
		t.Fatalf("want missing member error, got %v", err)
	}
}

func TestCheckOutdatedAndResolve(t *testing.T) {
	f := newFixture(t)
	m := testManifest(f, t)
	b := newBoot(t, m, config.DriverCHV, "amd64", nil)
	if err := b.Install(context.Background(), "juicefs"); err != nil {
		t.Fatal(err)
	}
	m.Binaries[0].Version = "3"
	statuses, err := b.Check()
	if err != nil {
		t.Fatal(err)
	}
	if !statuses[0].Outdated {
		t.Fatal("a manifest upgrade must mark the binary outdated")
	}
	// A user-provided binary without an install record is never outdated.
	os.WriteFile(filepath.Join(b.opts.BinDir, "cloud-hypervisor"), []byte("custom"), 0o755)
	statuses, _ = b.Check()
	if statuses[1].Outdated || statuses[1].Source != "runtime" {
		t.Fatalf("user binary status = %+v", statuses[1])
	}
	if p, err := b.Resolve("juicefs"); err != nil || p != filepath.Join(b.opts.BinDir, "juicefs") {
		t.Fatalf("Resolve = %q, %v", p, err)
	}
	b.opts.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	if p, _ := b.Resolve("virtiofsd"); p != "/usr/bin/virtiofsd" {
		t.Fatalf("PATH fallback = %q", p)
	}
}

func TestEmbeddedManifest(t *testing.T) {
	m, err := DefaultManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"juicefs", "cloud-hypervisor", "virtiofsd", "magpie", "cloudflared"} {
		bin, ok := m.Lookup(name)
		if !ok {
			t.Fatalf("manifest lacks %s", name)
		}
		for _, plat := range []string{"linux/amd64", "linux/arm64"} {
			a, ok := bin.Platforms[plat]
			if !ok {
				t.Fatalf("%s lacks %s", name, plat)
			}
			if a.URL != "" && !strings.HasPrefix(a.URL, "https://") {
				t.Errorf("%s %s: URL must be https", name, plat)
			}
			if a.URL != "" && a.SHA256 == "" && a.ChecksumSource != ChecksumUnverified {
				t.Errorf("%s %s: missing checksum must be marked %s", name, plat, ChecksumUnverified)
			}
			if _, known := m.ChecksumSources[a.ChecksumSource]; !known {
				t.Errorf("%s %s: undocumented checksumSource %q", name, plat, a.ChecksumSource)
			}
		}
	}
	// cloudflared is optional: Ensure must not download it for any driver.
	if cf, _ := m.Lookup("cloudflared"); cf.Required("chv") || cf.Required("docker") {
		t.Error("cloudflared must be optional")
	}
	if _, err := ParseManifest([]byte(`{"schemaVersion":2}`)); err == nil {
		t.Fatal("unknown schema version must fail")
	}
	bad := fmt.Sprintf(`{"schemaVersion":1,"binaries":[{"name":"x","version":"1","platforms":{"linux/amd64":{"url":"https://x","sha256":"%s","archive":"zip"}}}]}`, strings.Repeat("a", 64))
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("archive without member must fail")
	}
}

func TestFetchImages(t *testing.T) {
	f := newFixture(t)
	var sums strings.Builder
	for _, name := range GuestImages {
		body := []byte("image:" + name)
		f.add("/images/"+name, body)
		fmt.Fprintf(&sums, "%s  %s\n", sum(body), name)
	}
	f.add("/images/SHA256SUMS", []byte(sums.String()))
	b := newBoot(t, &Manifest{SchemaVersion: 1}, config.DriverCHV, "amd64", nil)
	dir := filepath.Join(t.TempDir(), "images")
	if ImagesPresent(dir) {
		t.Fatal("empty dir reported as present")
	}
	if err := b.FetchImages(context.Background(), f.srv.URL+"/images/", dir); err != nil {
		t.Fatal(err)
	}
	if !ImagesPresent(dir) {
		t.Fatal("images missing after fetch")
	}

	// A corrupted image aborts without replacing the installed set.
	f.files["/images/rootfs.squashfs"] = []byte("corrupt")
	err := b.FetchImages(context.Background(), f.srv.URL+"/images", dir)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want mismatch, got %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "rootfs.squashfs")); string(data) != "image:rootfs.squashfs" {
		t.Fatal("a failed fetch replaced an image")
	}
	if _, err := parseSHA256SUMS([]byte("nothex file\n")); err == nil {
		t.Fatal("malformed SHA256SUMS must fail")
	}
}
