//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/portwatcher/lydiabox/internal/config"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest"
	"github.com/portwatcher/lydiabox/internal/instabox/instaboxtest/fakeguest"
	vmmib "github.com/portwatcher/lydiabox/internal/vmm/instabox"
)

const cellToken = "cell-admin-token-0123456789abcdef"

// cellRig is a fake InstaBox whose machines run the real guest daemon,
// and the environment of one cell that uses it.
type cellRig struct {
	t        *testing.T
	root     string
	fake     *instaboxtest.Server
	runner   *fakeguest.Runner
	env      []string
	sock     string
	http     *http.Client
	upstream *recorder
}

// recorder is the upstream of the guest LLM gateway tunnel.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Clone(context.Background()))
	r.mu.Unlock()
	_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
}

func (r *recorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, q := range r.reqs {
		out = append(out, q.URL.Path)
	}
	return out
}

// checkGoroutines fails the test when goroutines that it started still
// run after every cleanup. Call it first, so that its cleanup runs last.
func checkGoroutines(t *testing.T) {
	t.Helper()
	base := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(15 * time.Second)
		for runtime.NumGoroutine() > base {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Errorf("goroutine leak: %d running, %d before the test\n%s", runtime.NumGoroutine(), base, buf[:n])
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}

func newCellRig(t *testing.T) *cellRig { return newCellRigWith(t, nil) }

// newCellRigWith is newCellRig with changes to the options of the fake.
func newCellRigWith(t *testing.T, fakeOpts func(*instaboxtest.Options)) *cellRig {
	t.Helper()
	root, err := os.MkdirTemp("", "lbxe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	r := &cellRig{t: t, root: root, runner: fakeguest.New(filepath.Join(root, "m"), nil), upstream: &recorder{}}
	opts := instaboxtest.Options{OpDelay: 10 * time.Millisecond, Guest: r.runner,
		Exec: func(id string, req instaboxtest.ExecRequest) instaboxtest.ExecResult {
			if strings.Contains(req.Cmd, "restart lydiabox-guest") {
				r.runner.Restart(id)
			}
			return instaboxtest.DefaultExec(id, req)
		}}
	if fakeOpts != nil {
		fakeOpts(&opts)
	}
	r.fake = instaboxtest.New(opts)
	t.Cleanup(func() {
		r.fake.Close()
		r.runner.Close()
	})
	up := &http.Server{Handler: r.upstream}
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = up.Serve(upLn) }()
	t.Cleanup(func() { up.Close() })
	t.Setenv(envGatewayUpstream, "http://"+upLn.Addr().String())

	tokenFile := filepath.Join(root, "token")
	if err := os.WriteFile(tokenFile, []byte(cellToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(root, "ibkey")
	if err := os.WriteFile(keyFile, []byte(r.fake.APIKey()), 0o600); err != nil {
		t.Fatal(err)
	}
	r.sock = filepath.Join(root, "cell.sock")
	r.env = []string{
		"LYDIABOX_CELL_MODE=true", "LYDIABOX_TENANT_ID=alice", "LYDIABOX_DATA_DIR=" + filepath.Join(root, "cell"),
		"LYDIABOX_LISTEN_ADDR=unix:" + r.sock, "LYDIABOX_AUTH_TOKEN_FILE=" + tokenFile,
		"LYDIABOX_MASTER_KEY=" + strings.Repeat("ab", 32), "LYDIABOX_S3_GATEWAY_ADDR=off",
		"LYDIABOX_MAGPIE_ADDR=" + freeAddr(t), "LYDIABOX_MAGPIE_WEB_ADDR=" + freeAddr(t),
		"LYDIABOX_STORAGE_MODE=provider", "LYDIABOX_INSTABOX_API_URL=" + r.fake.URL,
		"LYDIABOX_INSTABOX_API_KEY_FILE=" + keyFile, "LYDIABOX_INSTABOX_SNAPSHOT=" + r.fake.AddSnapshot("lydiabox-desktop"),
		"LYDIABOX_VM_CPUS=2", "LYDIABOX_VM_MEM_MB=4096", "LYDIABOX_IDLE_TIMEOUT=3s",
		"LYDIABOX_TURN_URLS=turn:turn.example.com:443?transport=tcp", "LYDIABOX_TURN_USERNAME=u", "LYDIABOX_TURN_PASSWORD=p",
		"LYDIABOX_WEBRTC_RELAY_ONLY=true",
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", r.sock)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	r.http = &http.Client{Transport: tr, Timeout: 60 * time.Second}
	prev := instaboxOptions
	instaboxOptions = func(o *vmmib.Options) {
		o.LinkMinBackoff, o.LinkMaxBackoff, o.LinkKeepAlive = 100*time.Millisecond, time.Second, time.Second
		o.PollInterval = time.Second
	}
	t.Cleanup(func() { instaboxOptions = prev })
	return r
}

// cell is one running `lydiabox up` process.
type cell struct {
	stopped bool
	cancel  context.CancelFunc
	done    chan error
	out     *bytes.Buffer
}

// start runs serve with the cell environment, as `lydiabox up` does, and
// waits until the cell answers.
func (r *cellRig) start() *cell {
	t := r.t
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{HomeDir: r.root, ConfigFile: os.DevNull, Environ: func() []string { return r.env }})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureSecrets(); err != nil {
		t.Fatal(err)
	}
	if cfg.AuthTokenGenerated || cfg.MasterKeyGenerated {
		t.Fatal("a cell generated a secret")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &cell{cancel: cancel, done: make(chan error, 1), out: &bytes.Buffer{}}
	go func() {
		c.done <- serve(ctx, Env{Stdout: c.out, Stderr: io.Discard}, cfg, slog.New(slog.DiscardHandler), true)
	}()
	t.Cleanup(func() {
		if !c.stopped {
			c.stop(t)
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, body := r.call(http.MethodGet, "/api/v1/ready", "")
		if code == http.StatusOK {
			var rd struct{ Status, TenantID string }
			if json.Unmarshal(body, &rd) != nil || rd.TenantID != "alice" {
				t.Fatalf("ready %s", body)
			}
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cell did not get ready: %d %s", code, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stop sends the cell its SIGTERM and waits for it.
func (c *cell) stop(t *testing.T) {
	t.Helper()
	c.stopped = true
	c.cancel()
	select {
	case err := <-c.done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the cell did not stop")
	}
}

// call makes an authenticated API request through the cell socket.
func (r *cellRig) call(method, path, body string) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://cell"+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+cellToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// must makes a request and fails the test on another status.
func (r *cellRig) must(method, path, body string, want int) []byte {
	r.t.Helper()
	code, b := r.call(method, path, body)
	if code != want {
		r.t.Fatalf("%s %s = %d %s, want %d", method, path, code, b, want)
	}
	return b
}

// vmState returns the VM state that the cell reports.
func (r *cellRig) vmState() (string, string) {
	r.t.Helper()
	var st struct{ State, LastError string }
	if err := json.Unmarshal(r.must(http.MethodGet, "/api/v1/vm/status", "", http.StatusOK), &st); err != nil {
		r.t.Fatal(err)
	}
	return st.State, st.LastError
}

func (r *cellRig) waitVM(want string) {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, lastErr := r.vmState()
		if st == want {
			return
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("VM state %s (%s), want %s", st, lastErr, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// dialWS opens a WebSocket of the cell API.
func (r *cellRig) dialWS(ctx context.Context, path, subprotocol string) *websocket.Conn {
	r.t.Helper()
	c, _, err := websocket.Dial(ctx, "ws://cell"+path, &websocket.DialOptions{HTTPClient: r.http, Subprotocols: []string{subprotocol},
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cellToken}}})
	if err != nil {
		r.t.Fatalf("dial %s: %v", path, err)
	}
	return c
}

// guestHTTP makes a request from inside the machine to a host port: through
// the UNIX bridge of the guest daemon, as harness processes do.
func (r *cellRig) guestHTTP(id string, port int, method, path string) (int, string) {
	r.t.Helper()
	sock := filepath.Join(r.runner.RunDir(id), "host-"+strconv.Itoa(port)+".sock")
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest(method, "http://host"+path, nil)
	resp, err := (&http.Client{Transport: tr, Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		r.t.Fatalf("guest request to host port %d: %v", port, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// machine returns the only machine of the fake.
func (r *cellRig) machine() string {
	r.t.Helper()
	ids := r.fake.Machines()
	if len(ids) != 1 {
		r.t.Fatalf("%d machines, want 1", len(ids))
	}
	return ids[0]
}
