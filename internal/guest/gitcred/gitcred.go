// Package gitcred is the guest side of the dynamic git credential helper.
// Git runs `lydiabox-guest git-cred get` (credential.helper); the helper
// asks the host connector broker for a fresh token over vsock port 9001 (or
// the docker driver's UNIX socket) and prints it to git on stdout. The token
// is held only in memory: nothing is written to the guest disk. `store` and
// `erase` are accepted and ignored because the host owns the credential.
package gitcred

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// HelperCommand is the credential.helper value that `git-cred install`
// writes to the guest's global git config.
const HelperCommand = "/usr/local/bin/lydiabox-guest git-cred"

// maxInput bounds what the helper reads from git.
const maxInput = 64 << 10

// Runner runs an external command (git for `install`).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSRunner runs commands with os/exec.
type OSRunner struct{}

// Run implements Runner and returns the combined output.
func (OSRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Options configures one helper invocation.
type Options struct {
	// HostAddr is LYDIABOX_HOST_ADDR.
	HostAddr string
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	// Runner runs git for `install` (defaults to OSRunner).
	Runner Runner
	// Dial overrides Dial (tests).
	Dial func(ctx context.Context, addr string) (net.Conn, error)
	// Timeout bounds the broker request (default 30s).
	Timeout time.Duration
}

const usage = "usage: lydiabox-guest git-cred <get|store|erase|install>"

// Run executes the helper action named by args[0].
func Run(ctx context.Context, args []string, opts Options) error {
	if len(args) != 1 {
		return errors.New(usage)
	}
	switch args[0] {
	case "get":
		return get(ctx, opts)
	case "store", "erase":
		// The host owns the credential; drain git's input and do nothing.
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(opts.Stdin, maxInput))
		}
		return nil
	case "install":
		return install(ctx, opts)
	}
	return fmt.Errorf("unknown action %q; %s", args[0], usage)
}

// Request holds the credential attributes git passed on stdin.
type Request struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	Username string `json:"username,omitempty"`
}

// ParseInput reads git's key=value lines up to a blank line or EOF.
func ParseInput(r io.Reader) (Request, error) {
	var req Request
	sc := bufio.NewScanner(io.LimitReader(r, maxInput))
	sc.Buffer(make([]byte, 0, 4096), maxInput)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "protocol":
			req.Protocol = v
		case "host":
			req.Host = v
		case "path":
			req.Path = v
		case "username":
			req.Username = v
		}
	}
	return req, sc.Err()
}

type brokerError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func get(ctx context.Context, opts Options) error {
	if opts.Stdin == nil {
		return errors.New("git-cred get: no input")
	}
	req, err := ParseInput(opts.Stdin)
	if err != nil {
		return fmt.Errorf("read git input: %w", err)
	}
	if req.Protocol != "https" || req.Host == "" {
		return nil // not ours: let git try the next helper
	}
	if strings.ContainsAny(req.Host+req.Path+req.Username, "\x00\n") {
		return errors.New("git input contains invalid characters")
	}
	if opts.HostAddr == "" {
		return ErrNoHostAddr
	}
	dial := opts.Dial
	if dial == nil {
		dial = Dial
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{
		DialContext:       func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx, opts.HostAddr) },
		DisableKeepAlives: true,
	}}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://lydiabox-host/git-cred", bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return fmt.Errorf("contact host broker: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxInput))
	if err != nil {
		return fmt.Errorf("read host broker answer: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil // no connected account for this host
	default:
		var be brokerError
		_ = json.Unmarshal(raw, &be)
		return fmt.Errorf("host broker: HTTP %d: %s", resp.StatusCode, be.Error.Message)
	}
	var cred struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &cred); err != nil || cred.Password == "" {
		return errors.New("host broker returned an invalid credential")
	}
	if strings.ContainsAny(cred.Username+cred.Password, "\x00\n") {
		return errors.New("host broker returned an invalid credential")
	}
	_, err = fmt.Fprintf(opts.Stdout, "username=%s\npassword=%s\n", cred.Username, cred.Password)
	return err
}

func install(ctx context.Context, opts Options) error {
	r := opts.Runner
	if r == nil {
		r = OSRunner{}
	}
	out, err := r.Run(ctx, "git", "config", "--global", "--replace-all", "credential.helper", HelperCommand)
	if err != nil {
		return fmt.Errorf("git config: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if opts.Stdout != nil {
		fmt.Fprintf(opts.Stdout, "credential.helper = %s\n", HelperCommand)
	}
	return nil
}
