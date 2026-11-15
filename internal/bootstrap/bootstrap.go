package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/lydiabox/lydiabox/internal/config"
)

// installRecordFile records which manifest version installed each binary so
// that a manifest upgrade replaces stale binaries but never touches binaries
// the user placed in the directory by hand.
const installRecordFile = ".installed.json"

// Options configures a Bootstrapper.
type Options struct {
	// BinDir is $DATA_DIR/runtime/bin.
	BinDir string
	// Driver is the resolved VM driver (chv, docker, or instabox); it selects which
	// binaries are required.
	Driver string
	// Skip names binaries that are not required in this configuration (for
	// example juicefs in the provider storage mode).
	Skip []string
	// GOOS and GOARCH default to the running platform.
	GOOS, GOARCH string
	// Mirrors overrides download URLs and checksums per binary.
	Mirrors map[string]config.Mirror
	// Manifest defaults to the embedded manifest.json.
	Manifest *Manifest
	// HTTPClient defaults to a client with a 30 minute overall timeout.
	HTTPClient *http.Client
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// LookPath finds binaries on PATH; defaults to exec.LookPath. Tests set it
	// to a stub that always fails.
	LookPath func(string) (string, error)
	// MaxDownloadBytes caps a single download (default 512 MiB).
	MaxDownloadBytes int64
}

// Bootstrapper checks and installs runtime binaries.
type Bootstrapper struct {
	opts Options
}

// New validates options and fills defaults.
func New(opts Options) (*Bootstrapper, error) {
	if opts.BinDir == "" {
		return nil, errors.New("bootstrap: BinDir is required")
	}
	if opts.Driver != config.DriverCHV && opts.Driver != config.DriverDocker && opts.Driver != config.DriverInstabox {
		return nil, fmt.Errorf("bootstrap: driver must be resolved to chv, docker, or instabox, got %q", opts.Driver)
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	if opts.Manifest == nil {
		m, err := DefaultManifest()
		if err != nil {
			return nil, err
		}
		opts.Manifest = m
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Minute}
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.MaxDownloadBytes <= 0 {
		opts.MaxDownloadBytes = 512 << 20
	}
	if opts.Mirrors == nil {
		opts.Mirrors = map[string]config.Mirror{}
	}
	return &Bootstrapper{opts: opts}, nil
}

// Platform returns the GOOS/GOARCH key used in the manifest.
func (b *Bootstrapper) Platform() string { return b.opts.GOOS + "/" + b.opts.GOARCH }

// Status describes one runtime binary.
type Status struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Required bool   `json:"required"`
	// Path is where the binary was found; empty when missing.
	Path string `json:"path,omitempty"`
	// Source is "runtime" (BinDir), "path" (found on PATH), or "missing".
	Source string `json:"source"`
	// Outdated is true when BinDir holds a version installed from an older manifest.
	Outdated bool `json:"outdated,omitempty"`
}

// Check reports the state of every binary in the manifest without changing anything.
func (b *Bootstrapper) Check() ([]Status, error) {
	rec, err := b.readRecord()
	if err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(b.opts.Manifest.Binaries))
	for _, bin := range b.opts.Manifest.Binaries {
		st := Status{Name: bin.Name, Version: bin.Version, Required: bin.Required(b.opts.Driver) && !slices.Contains(b.opts.Skip, bin.Name), Source: "missing"}
		local := filepath.Join(b.opts.BinDir, bin.Name)
		if isExecutable(local) {
			st.Path, st.Source = local, "runtime"
			if r, ok := rec[bin.Name]; ok && r.Version != bin.Version {
				st.Outdated = true
			}
		} else if p, err := b.opts.LookPath(bin.Name); err == nil {
			st.Path, st.Source = p, "path"
		}
		out = append(out, st)
	}
	return out, nil
}

// Ensure installs every required binary that is missing or outdated and
// returns the resulting status. Optional binaries are left alone.
func (b *Bootstrapper) Ensure(ctx context.Context) ([]Status, error) {
	if err := os.MkdirAll(b.opts.BinDir, 0o755); err != nil {
		return nil, fmt.Errorf("bootstrap: create %s: %w", b.opts.BinDir, err)
	}
	statuses, err := b.Check()
	if err != nil {
		return nil, err
	}
	var errs []error
	for i, st := range statuses {
		if !st.Required || (st.Source != "missing" && !st.Outdated) {
			continue
		}
		if st.Outdated && b.builtLocally(st.Name) {
			// A locally built binary cannot be replaced by a download; keep it.
			b.opts.Logger.Warn("a locally built runtime binary is older than the manifest; rebuild it",
				"name", st.Name, "version", st.Version, "path", filepath.Join(b.opts.BinDir, st.Name))
			continue
		}
		if err := b.Install(ctx, st.Name); err != nil {
			errs = append(errs, err)
			continue
		}
		statuses[i].Path = filepath.Join(b.opts.BinDir, st.Name)
		statuses[i].Source = "runtime"
		statuses[i].Outdated = false
	}
	return statuses, errors.Join(errs...)
}

// Resolve returns the path of a binary in BinDir or on PATH.
func (b *Bootstrapper) Resolve(name string) (string, error) {
	local := filepath.Join(b.opts.BinDir, name)
	if isExecutable(local) {
		return local, nil
	}
	if p, err := b.opts.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("bootstrap: %s is not installed in %s or on PATH", name, b.opts.BinDir)
}

// Install downloads, verifies, and installs one binary unconditionally.
func (b *Bootstrapper) Install(ctx context.Context, name string) error {
	bin, ok := b.opts.Manifest.Lookup(name)
	if !ok {
		return fmt.Errorf("bootstrap: unknown binary %q", name)
	}
	src, err := b.source(bin)
	if err != nil {
		return err
	}
	b.opts.Logger.Info("downloading runtime binary", "name", name, "version", bin.Version, "url", src.URL)
	if err := os.MkdirAll(b.opts.BinDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap: create %s: %w", b.opts.BinDir, err)
	}
	if err := b.fetchAndInstall(ctx, name, src); err != nil {
		return fmt.Errorf("bootstrap: install %s %s: %w", name, bin.Version, err)
	}
	return b.writeRecord(name, installRecord{Version: bin.Version, SHA256: src.SHA256, URL: src.URL})
}

// source merges the manifest artifact with any LYDIABOX_MIRROR_* override.
func (b *Bootstrapper) source(bin Binary) (Artifact, error) {
	art := bin.Platforms[b.Platform()]
	if m, ok := b.opts.Mirrors[bin.Name]; ok {
		if m.URL != "" {
			art.URL = m.URL
			if art.Archive == "" {
				art.Archive = ArchiveRaw
			}
		}
		if m.SHA256 != "" {
			art.SHA256 = m.SHA256
		}
	}
	env := config.MirrorEnvName(bin.Name)
	if art.URL == "" && art.BuiltLocally() {
		return art, fmt.Errorf("bootstrap: %s %s for %s is built from source, not downloaded: run 'lydiabox runtime build-rootfs' "+
			"(or '%s' and 'lydiabox runtime install-local %s dist/%s-%s'), install it on PATH, or set %s and %s_SHA256",
			bin.Name, bin.Version, b.Platform(), art.Build, bin.Name, bin.Name, b.opts.GOOS+"-"+b.opts.GOARCH, env, env)
	}
	if art.URL == "" {
		return art, fmt.Errorf("bootstrap: %s has no download for %s; install it on PATH or set %s and %s_SHA256",
			bin.Name, b.Platform(), env, env)
	}
	if art.SHA256 == "" {
		return art, fmt.Errorf("bootstrap: %s for %s has no pinned SHA256 (checksumSource %s); fill internal/bootstrap/manifest.json or set %s_SHA256",
			bin.Name, b.Platform(), ChecksumUnverified, env)
	}
	return art, nil
}

type installRecord struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	URL     string `json:"url"`
}

func (b *Bootstrapper) readRecord() (map[string]installRecord, error) {
	data, err := os.ReadFile(filepath.Join(b.opts.BinDir, installRecordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]installRecord{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("bootstrap: read install record: %w", err)
	}
	rec := map[string]installRecord{}
	if err := json.Unmarshal(data, &rec); err != nil {
		// A corrupt record only loses upgrade tracking; start over.
		return map[string]installRecord{}, nil
	}
	return rec, nil
}

func (b *Bootstrapper) writeRecord(name string, r installRecord) error {
	rec, err := b.readRecord()
	if err != nil {
		return err
	}
	rec[name] = r
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(b.opts.BinDir, installRecordFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("bootstrap: write install record: %w", err)
	}
	return os.Rename(tmp, path)
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
