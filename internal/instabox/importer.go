package instabox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The REST API has no import endpoint: `instabox snapshot import` is a
// CLI flow (a throwaway agent-browser machine pulls the image with
// Docker, lays its files over the machine's own, restarts it, saves a
// snapshot, and deletes the machine). ImportSnapshot runs that CLI.
// https://instabox.sh/docs/machines/custom-images.md

// ImportOptions configures ImportSnapshot.
type ImportOptions struct {
	// Image is a registry reference of a linux/amd64 image. The import
	// pulls it; it cannot read a local Docker image.
	Image string
	// Name is the snapshot name (1-63 of a-z, 0-9, -).
	Name string
	// CLI is the instabox binary (default "instabox" on PATH).
	CLI string
	// RegistryUsername and RegistryPassword sign in to a private
	// registry. The password goes to the CLI in INSTABOX_REGISTRY_PASSWORD,
	// never on the command line.
	RegistryUsername string
	RegistryPassword string
	// Timeout bounds the pull and the overlay (CLI default 30m).
	Timeout time.Duration
	// NoVerify skips the restart check; KeepBuilder keeps the throwaway
	// machine for debugging.
	NoVerify    bool
	KeepBuilder bool
	// Progress receives the CLI's standard error (nil discards it).
	Progress io.Writer
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ImportArgs returns the CLI arguments (after the binary) of an import,
// without credentials.
func ImportArgs(opts ImportOptions) []string {
	args := []string{"snapshot", "import", opts.Image, "--name", opts.Name, "--json"}
	if opts.RegistryUsername != "" {
		args = append(args, "--registry-username", opts.RegistryUsername)
	}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", opts.Timeout.String())
	}
	if opts.NoVerify {
		args = append(args, "--no-verify")
	}
	if opts.KeepBuilder {
		args = append(args, "--keep-builder")
	}
	return args
}

// ImportSnapshot imports an OCI image as a snapshot with the instabox CLI
// and returns the snapshot, read back through the API by name. The CLI
// gets this client's API key and base URL in its environment.
func (c *Client) ImportSnapshot(ctx context.Context, opts ImportOptions) (*Snapshot, error) {
	if strings.TrimSpace(opts.Image) == "" {
		return nil, errors.New("instabox: import needs an image reference")
	}
	if !nameRE.MatchString(opts.Name) {
		return nil, fmt.Errorf("instabox: invalid snapshot name %q", opts.Name)
	}
	cli := opts.CLI
	if cli == "" {
		cli = "instabox"
	}
	cmd := exec.CommandContext(ctx, cli, ImportArgs(opts)...)
	cmd.Env = append(cliEnv(os.Environ()),
		"INSTABOX_API_KEY="+c.apiKey,
		"INSTABOX_API_URL="+c.base,
		"INSTABOX_NO_UPDATE_CHECK=1",
		"INSTABOX_NO_AUTO_JSON=1",
	)
	if opts.RegistryPassword != "" {
		cmd.Env = append(cmd.Env, "INSTABOX_REGISTRY_PASSWORD="+opts.RegistryPassword)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = opts.Progress
	if cmd.Stderr == nil {
		cmd.Stderr = io.Discard
	}
	runErr := cmd.Run()
	if apiErr := cliError(stdout.Bytes()); apiErr != nil {
		apiErr.Method, apiErr.Path = "CLI", "snapshot import"
		return nil, apiErr
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("instabox: snapshot import of %s: %w", opts.Image, runErr)
	}
	s, err := c.FindSnapshot(ctx, opts.Name)
	if err != nil {
		return nil, fmt.Errorf("instabox: snapshot import of %s finished, but the snapshot is not listed: %w", opts.Image, err)
	}
	return s, nil
}

// cliEnv drops InstaBox credentials and settings of the calling process,
// so the CLI uses only what ImportSnapshot passes.
func cliEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "INSTABOX_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// cliError reads the {"error": {...}} object the CLI prints with --json
// when it fails.
func cliError(out []byte) *APIError {
	out = bytes.TrimSpace(out)
	if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
		out = out[i+1:]
	}
	var body errorBody
	if json.Unmarshal(out, &body) != nil || body.Error == nil || body.Error.Code == "" {
		return nil
	}
	e := &APIError{ErrorDetail: *body.Error}
	e.RetryAfter = time.Duration(e.RetryAfterSeconds) * time.Second
	return e
}
