package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lydiabox/lydiabox/internal/config"
)

// fakeRunner simulates juicefs and mkfs.ext4 without the real binaries.
type fakeRunner struct {
	mu       sync.Mutex
	cfg      *config.Config
	runs     []Command
	starts   []Command
	stdin    map[string][]byte // subcommand -> stdin content
	dump     string            // output of `juicefs dump`
	failRun  map[string]error  // subcommand -> error
	procs    []*fakeProc
	mountDir string
}

func newFakeRunner(cfg *config.Config) *fakeRunner {
	return &fakeRunner{cfg: cfg, stdin: map[string][]byte{}, failRun: map[string]error{},
		dump: `{"Setting":{"Name":"lydiabox-default"}}`, mountDir: cfg.MountDir()}
}

func sub(c Command) string {
	if filepath.Base(c.Path) == "mkfs.ext4" {
		return "mkfs"
	}
	if len(c.Args) == 0 {
		return ""
	}
	return c.Args[0]
}

func (f *fakeRunner) Run(ctx context.Context, c Command) error {
	f.mu.Lock()
	f.runs = append(f.runs, c)
	err := f.failRun[sub(c)]
	dump := f.dump
	f.mu.Unlock()
	if c.Stdin != nil {
		data, _ := io.ReadAll(c.Stdin)
		f.mu.Lock()
		f.stdin[sub(c)] = data
		f.mu.Unlock()
	}
	if err != nil {
		if sub(c) == "format" || sub(c) == "load" {
			os.WriteFile(f.cfg.JuiceFSMetaPath(), []byte("partial"), 0o600)
		}
		return err
	}
	switch sub(c) {
	case "format", "load":
		return os.WriteFile(f.cfg.JuiceFSMetaPath(), []byte("sqlite"), 0o600)
	case "dump":
		_, err := io.WriteString(c.Stdout, dump)
		return err
	case "umount":
		f.unmount()
	case "mkfs":
		img := c.Args[len(c.Args)-1]
		if _, err := os.Stat(img); err != nil {
			return fmt.Errorf("mkfs: %w", err)
		}
	}
	return nil
}

func (f *fakeRunner) Start(c Command) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts = append(f.starts, c)
	p := &fakeProc{pid: 1000 + len(f.procs), exit: make(chan error, 1), onExit: func() {}}
	if sub(c) == "mount" {
		if err := os.WriteFile(filepath.Join(f.mountDir, ".config"), []byte("{}"), 0o644); err != nil {
			return nil, err
		}
		p.onExit = func() { os.Remove(filepath.Join(f.mountDir, ".config")) }
	}
	f.procs = append(f.procs, p)
	return p, nil
}

// unmount ends the running mount process like a real `juicefs umount`.
func (f *fakeRunner) unmount() {
	f.mu.Lock()
	var mount *fakeProc
	for i, c := range f.starts {
		if sub(c) == "mount" {
			mount = f.procs[i]
		}
	}
	f.mu.Unlock()
	if mount != nil {
		mount.finish(nil)
	}
}

func (f *fakeRunner) subcommands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.runs {
		out = append(out, sub(c))
	}
	return out
}

func (f *fakeRunner) findRun(name string) (Command, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.runs {
		if sub(c) == name {
			return c, true
		}
	}
	return Command{}, false
}

type fakeProc struct {
	pid    int
	once   sync.Once
	exit   chan error
	onExit func()
	mu     sync.Mutex
	sigs   []os.Signal
}

func (p *fakeProc) Pid() int { return p.pid }
func (p *fakeProc) Signal(s os.Signal) error {
	p.mu.Lock()
	p.sigs = append(p.sigs, s)
	p.mu.Unlock()
	p.finish(errors.New("signal: terminated"))
	return nil
}
func (p *fakeProc) Kill() error { p.finish(errors.New("killed")); return nil }
func (p *fakeProc) Wait() error { return <-p.exit }
func (p *fakeProc) finish(err error) {
	p.once.Do(func() {
		p.onExit()
		p.exit <- err
	})
}

// memObjects is an in-memory ObjectStore.
type memObjects struct {
	mu   sync.Mutex
	objs map[string][]byte
	puts map[string]int
}

func newMemObjects() *memObjects {
	return &memObjects{objs: map[string][]byte{}, puts: map[string]int{}}
}

func (m *memObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memObjects) Put(_ context.Context, key string, body io.ReadSeeker, size int64, _ string) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return fmt.Errorf("size mismatch: %d != %d", len(data), size)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = data
	m.puts[key]++
	return nil
}

func (m *memObjects) putCount(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.puts[key]
}

// fakeState implements Snapshotter.
type fakeState struct {
	mu      sync.Mutex
	content string
}

func (s *fakeState) VacuumInto(_ context.Context, dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.WriteFile(dest, []byte(s.content), 0o600)
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	c := config.Defaults(t.TempDir())
	c.DataDir = t.TempDir()
	c.S3 = config.S3Config{Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto", Bucket: "bkt",
		AccessKey: "AKID", SecretKey: "SECRET"}
	return c
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
