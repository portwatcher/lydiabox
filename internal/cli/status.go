package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lydiabox/lydiabox/internal/api"
	"github.com/lydiabox/lydiabox/internal/config"
	"github.com/lydiabox/lydiabox/internal/version"
)

func runStatus(ctx context.Context, env Env, args []string) error {
	fs := newFlagSet("status", env)
	configFile := fs.String("config", "", "path to lydiabox.yaml")
	addr := fs.String("addr", "", "server address (default: derived from LYDIABOX_LISTEN_ADDR)")
	asJSON := fs.Bool("json", false, "print the raw JSON status")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	cfg, err := config.Load(config.LoadOptions{ConfigFile: *configFile})
	if err != nil {
		return err
	}
	token, err := readToken(cfg)
	if err != nil {
		return err
	}
	base := *addr
	if base == "" {
		base = clientAddr(cfg.ListenAddr)
	}
	client := http.DefaultClient
	if sock, ok := config.UnixListenPath(base); ok {
		client = unixClient(sock)
		base = "http://cell"
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/v1/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("server is not reachable at %s: %w", base, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if *asJSON {
		_, err := env.Stdout.Write(body)
		return err
	}
	var st api.StatusResponse
	if err := json.Unmarshal(body, &st); err != nil {
		return fmt.Errorf("decode status: %w", err)
	}
	printStatus(env.Stdout, base, st)
	return nil
}

func printStatus(w io.Writer, base string, st api.StatusResponse) {
	fmt.Fprintf(w, "Server:   %s (%s)\n", base, st.Version.Version)
	fmt.Fprintf(w, "Tenant:   %s\n", st.TenantID)
	fmt.Fprintf(w, "VM:       %s (driver %s)\n", st.VM.State, st.VM.Driver)
	mounted := "not mounted"
	switch {
	case st.Storage.Mode == config.StorageProvider:
		mounted = "machine disk (provider mode)"
	case st.Storage.Mounted:
		mounted = "mounted"
	}
	fmt.Fprintf(w, "Storage:  %s\n", mounted)
	switch {
	case st.Storage.Mode == config.StorageProvider && !st.Storage.SyncEnabled &&
		st.Storage.LastSyncError == "" && st.Storage.LastSyncAt.IsZero():
		fmt.Fprintln(w, "S3 sync:  off (no S3 settings)")
	case st.Storage.LastSyncError != "":
		fmt.Fprintf(w, "S3 sync:  failed: %s\n", st.Storage.LastSyncError)
	case st.Storage.LastSyncAt.IsZero():
		fmt.Fprintln(w, "S3 sync:  not yet")
	default:
		fmt.Fprintf(w, "S3 sync:  %s ago\n", st.Time.Sub(st.Storage.LastSyncAt).Round(time.Second))
	}
}

// readToken returns the admin token without creating one.
func readToken(cfg *config.Config) (string, error) {
	if cfg.AuthToken != "" {
		return cfg.AuthToken, nil
	}
	data, err := os.ReadFile(cfg.AuthTokenPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no admin token at %s; start the server with 'lydiabox up' first or set LYDIABOX_AUTH_TOKEN", cfg.AuthTokenPath())
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// unixClient sends every request to the UNIX socket at path.
func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}}
}

// clientAddr turns a wildcard listen address into a loopback address.
func clientAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func runVersion(_ context.Context, env Env, args []string) error {
	fs := newFlagSet("version", env)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return errUsage
	}
	info := version.Get()
	if *asJSON {
		return json.NewEncoder(env.Stdout).Encode(info)
	}
	_, err := fmt.Fprintf(env.Stdout, "lydiabox %s\n", info)
	return err
}
