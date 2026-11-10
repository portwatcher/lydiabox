package chv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lydiabox/lydiabox/internal/storage"
)

// fakeProcess is a storage.Process that ends when stopped.
type fakeProcess struct {
	once   sync.Once
	done   chan struct{}
	onStop func()
}

func newFakeProcess(onStop func()) *fakeProcess {
	return &fakeProcess{done: make(chan struct{}), onStop: onStop}
}

func (p *fakeProcess) end() {
	p.once.Do(func() {
		if p.onStop != nil {
			p.onStop()
		}
		close(p.done)
	})
}
func (p *fakeProcess) Pid() int               { return 4242 }
func (p *fakeProcess) Signal(os.Signal) error { p.end(); return nil }
func (p *fakeProcess) Kill() error            { p.end(); return nil }
func (p *fakeProcess) Wait() error            { <-p.done; return nil }

// mockCHV is a Cloud Hypervisor API server on a UNIX socket.
type mockCHV struct {
	mu       sync.Mutex
	requests []string
	created  json.RawMessage
	state    string
	srv      *http.Server
	proc     *fakeProcess
}

func (m *mockCHV) record(r string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, r)
}

func (m *mockCHV) Requests() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.requests, ",")
}

func (m *mockCHV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ep := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	if ep != "vmm.ping" && ep != "vm.info" {
		m.record(r.Method + " " + ep)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := func(want, next string) {
		if m.state != want {
			http.Error(w, "invalid state "+m.state, http.StatusInternalServerError)
			return
		}
		m.state = next
		w.WriteHeader(http.StatusNoContent)
	}
	switch {
	case r.Method == http.MethodGet && ep == "vmm.ping":
		_, _ = io.WriteString(w, `{"version":"v48.0"}`)
	case r.Method == http.MethodGet && ep == "vm.info":
		if m.state == "" {
			http.Error(w, "VM is not created", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": m.state, "config": json.RawMessage(m.created)})
	case r.Method == http.MethodPut && ep == "vm.create":
		b, _ := io.ReadAll(r.Body)
		m.created = b
		set("", "Created")
	case r.Method == http.MethodPut && ep == "vm.boot":
		set("Created", "Running")
	case r.Method == http.MethodPut && ep == "vm.pause":
		set("Running", "Paused")
	case r.Method == http.MethodPut && ep == "vm.resume":
		set("Paused", "Running")
	case r.Method == http.MethodPut && ep == "vm.shutdown":
		m.state = "Shutdown"
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut && ep == "vmm.shutdown":
		w.WriteHeader(http.StatusNoContent)
		go m.proc.end()
	default:
		http.NotFound(w, r)
	}
}

// fakeRunner starts mock daemons and records commands.
type fakeRunner struct {
	mu     sync.Mutex
	runs   []string
	starts []storage.Command
	nft    string
	chv    *mockCHV
}

func (f *fakeRunner) Run(_ context.Context, c storage.Command) error {
	line := filepath.Base(c.Path) + " " + strings.Join(c.Args, " ")
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		f.mu.Lock()
		f.nft = string(b)
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.runs = append(f.runs, line)
	f.mu.Unlock()
	if strings.HasPrefix(line, "nft delete") {
		return errors.New("nft: Error: No such file or directory")
	}
	return nil
}

func (f *fakeRunner) Start(c storage.Command) (storage.Process, error) {
	f.mu.Lock()
	f.starts = append(f.starts, c)
	f.mu.Unlock()
	switch filepath.Base(c.Path) {
	case "virtiofsd":
		var sock string
		for _, a := range c.Args {
			if v, ok := strings.CutPrefix(a, "--socket-path="); ok {
				sock = v
			}
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return nil, err
		}
		return newFakeProcess(func() { ln.Close() }), nil
	case "cloud-hypervisor":
		sock := strings.TrimPrefix(c.Args[1], "path=")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			return nil, err
		}
		m := &mockCHV{}
		m.srv = &http.Server{Handler: m}
		m.proc = newFakeProcess(func() { m.srv.Close() })
		go m.srv.Serve(ln)
		f.mu.Lock()
		f.chv = m
		f.mu.Unlock()
		return m.proc, nil
	}
	return nil, errors.New("unexpected start " + c.Path)
}

func (f *fakeRunner) Runs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.runs, "\n")
}
