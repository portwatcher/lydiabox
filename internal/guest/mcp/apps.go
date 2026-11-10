package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// BlenderRequest runs Python code in Blender.
type BlenderRequest struct {
	Code           string
	BlendFile      string
	TimeoutSeconds int
}

// BlenderResult is the captured Blender output.
type BlenderResult struct {
	Output   string
	ExitCode int
}

// LocalAppsOptions configures LocalApps.
type LocalAppsOptions struct {
	// Display is the X display (default ":0").
	Display string
	// CDPPort is Chrome's remote debugging port (default 9222).
	CDPPort int
	// ChromeUserDataDir is the persistent Chrome profile
	// (default /home/agent/.config/google-chrome).
	ChromeUserDataDir string
	// ChromeFlags are added to every Chrome launch (for example
	// --no-sandbox inside an unprivileged container).
	ChromeFlags []string
	// WorkDir is the working directory of launched applications.
	WorkDir string
	// Start launches a detached process (tests replace it).
	Start func(path string, args, env []string, dir string) error
	// LookPath resolves executables (tests replace it).
	LookPath func(string) (string, error)
	// Run runs a process to completion with combined output (tests).
	Run func(ctx context.Context, path string, args []string, env []string) ([]byte, int, error)
	// HTTPClient queries the CDP endpoint.
	HTTPClient *http.Client
}

// LocalApps launches applications on the guest desktop.
type LocalApps struct {
	opts     LocalAppsOptions
	chromeMu sync.Mutex
	// chromeStart is the time (Unix nanoseconds) of the last Chrome start.
	chromeStart atomic.Int64
}

var (
	chromeCandidates   = []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser"}
	terminalCandidates = []string{"xfce4-terminal", "x-terminal-emulator", "xterm"}
	blenderCandidates  = []string{"blender"}
)

// NewLocalApps returns the default application launcher.
func NewLocalApps(opts LocalAppsOptions) *LocalApps {
	if opts.Display == "" {
		opts.Display = ":0"
	}
	if opts.CDPPort == 0 {
		opts.CDPPort = 9222
	}
	if opts.ChromeUserDataDir == "" {
		opts.ChromeUserDataDir = "/home/agent/.config/google-chrome"
	}
	if opts.Start == nil {
		opts.Start = startDetached
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Run == nil {
		opts.Run = runCombined
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 3 * time.Second}
	}
	return &LocalApps{opts: opts}
}

func (a *LocalApps) find(candidates []string) (string, error) {
	for _, c := range candidates {
		if p, err := a.opts.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("none of %s is installed", strings.Join(candidates, ", "))
}

func (a *LocalApps) env() []string {
	env := os.Environ()
	return append(env, "DISPLAY="+a.opts.Display)
}

// ChromeArgs returns the Chrome command line used for every launch.
func (a *LocalApps) ChromeArgs() []string {
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", a.opts.CDPPort),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + a.opts.ChromeUserDataDir,
		"--no-first-run", "--no-default-browser-check", "--start-maximized",
	}
	return append(args, a.opts.ChromeFlags...)
}

// Launch implements Apps.
func (a *LocalApps) Launch(ctx context.Context, app string) error {
	switch app {
	case "chrome":
		return a.startChrome()
	case "blender":
		p, err := a.find(blenderCandidates)
		if err != nil {
			return err
		}
		return a.opts.Start(p, nil, a.env(), a.opts.WorkDir)
	case "terminal":
		p, err := a.find(terminalCandidates)
		if err != nil {
			return err
		}
		return a.opts.Start(p, nil, a.env(), a.opts.WorkDir)
	}
	return fmt.Errorf("app must be chrome, blender, or terminal")
}

func (a *LocalApps) startChrome() error {
	p, err := a.find(chromeCandidates)
	if err != nil {
		return err
	}
	if err := a.opts.Start(p, a.ChromeArgs(), a.env(), a.opts.WorkDir); err != nil {
		return err
	}
	a.chromeStart.Store(time.Now().UnixNano())
	return nil
}

func (a *LocalApps) cdpVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", a.opts.CDPPort), nil)
	if err != nil {
		return "", err
	}
	resp, err := a.opts.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("CDP /json/version: HTTP %d", resp.StatusCode)
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	if v.WebSocketDebuggerURL == "" {
		return "", errors.New("CDP /json/version has no webSocketDebuggerUrl")
	}
	return v.WebSocketDebuggerURL, nil
}

// CDPEndpoint implements Apps.
func (a *LocalApps) CDPEndpoint(ctx context.Context) (string, error) {
	if ws, err := a.cdpVersion(ctx); err == nil {
		return ws, nil
	}
	a.chromeMu.Lock()
	defer a.chromeMu.Unlock()
	if ws, err := a.cdpVersion(ctx); err == nil {
		return ws, nil
	}
	// A Chrome that is still starting holds the profile lock. Starting
	// another one only adds load (it hands its command line to the first
	// one and exits), so wait for the running one.
	if !a.chromeRunning() {
		if err := a.startChrome(); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("chrome did not open the CDP port %d: %v", a.opts.CDPPort, lastErr)
		case <-t.C:
			ws, err := a.cdpVersion(ctx)
			if err == nil {
				return ws, nil
			}
			lastErr = err
		}
	}
}

// chromeStartGrace is how long a Chrome started here counts as starting
// before it has made its profile lock.
const chromeStartGrace = 3 * time.Minute

// chromeRunning reports whether a Chrome started here is in its start
// period, or a live process holds the Chrome profile lock. Chrome makes
// <user-data-dir>/SingletonLock a symbolic link to "<hostname>-<pid>". A
// second Chrome on the same profile that gets no answer from the first one
// kills it, so a slow start must not be interrupted.
func (a *LocalApps) chromeRunning() bool {
	if t := a.chromeStart.Load(); t != 0 && time.Since(time.Unix(0, t)) < chromeStartGrace {
		return true
	}
	target, err := os.Readlink(filepath.Join(a.opts.ChromeUserDataDir, "SingletonLock"))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(target, '-')
	if i < 0 {
		return false
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// RunBlender implements Apps: blender --background [file] --python <script>.
func (a *LocalApps) RunBlender(ctx context.Context, req BlenderRequest) (BlenderResult, error) {
	p, err := a.find(blenderCandidates)
	if err != nil {
		return BlenderResult{}, err
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	timeout = min(timeout, time.Hour)
	f, err := os.CreateTemp("", "lydiabox-blender-*.py")
	if err != nil {
		return BlenderResult{}, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(req.Code); err != nil {
		f.Close()
		return BlenderResult{}, err
	}
	if err := f.Close(); err != nil {
		return BlenderResult{}, err
	}
	args := []string{"--background"}
	if req.BlendFile != "" {
		args = append(args, req.BlendFile)
	}
	args = append(args, "--python-exit-code", "1", "--python", f.Name())
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, code, err := a.opts.Run(ctx, p, args, a.env())
	if err != nil {
		return BlenderResult{}, err
	}
	const maxOut = 64 << 10
	if len(out) > maxOut {
		out = append([]byte("...(truncated)\n"), out[len(out)-maxOut:]...)
	}
	return BlenderResult{Output: string(out), ExitCode: code}, nil
}

func startDetached(path string, args, env []string, dir string) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func runCombined(ctx context.Context, path string, args, env []string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if ctx.Err() != nil {
				return out, ee.ExitCode(), fmt.Errorf("blender timed out: %w", ctx.Err())
			}
			return out, ee.ExitCode(), nil
		}
		return out, -1, err
	}
	return out, 0, nil
}
