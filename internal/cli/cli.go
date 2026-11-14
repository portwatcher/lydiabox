// Package cli implements the lydiabox host command line: up, status,
// runtime, cloudflare, and version. cmd/lydiabox/main.go only calls Main.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
)

// Env carries the process streams so commands are testable.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
}

type command struct {
	summary string
	run     func(ctx context.Context, env Env, args []string) error
}

var commands = map[string]command{
	"up":         {"Start the control plane (storage, API, web UI)", runUp},
	"status":     {"Show the status of a running control plane", runStatus},
	"runtime":    {"Manage runtime binaries and guest images", runRuntime},
	"cloudflare": {"Set up remote access through Cloudflare Zero Trust", runCloudflare},
	"version":    {"Print version information", runVersion},
}

// errUsage marks errors that already printed usage text.
var errUsage = errors.New("usage error")

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, env Env) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(env.Stdout)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "--version" || args[0] == "-v" {
		args = []string{"version"}
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "lydiabox: unknown command %q\n\n", args[0])
		usage(env.Stderr)
		return 2
	}
	if err := cmd.run(ctx, env, args[1:]); err != nil {
		if errors.Is(err, errUsage) || errors.Is(err, flag.ErrHelp) {
			return 2
		}
		fmt.Fprintf(env.Stderr, "lydiabox %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: lydiabox <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "  %-10s %s\n", n, commands[n].summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Configuration comes from LYDIABOX_* environment variables and an optional")
	fmt.Fprintln(w, "lydiabox.yaml (see .env.example). Run 'lydiabox <command> -h' for flags.")
}

func newFlagSet(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet("lydiabox "+name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// newLogger builds the process logger for a configured level.
func newLogger(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// stderrOrDiscard keeps tests quiet when Stderr is nil.
func stderrOrDiscard(env Env) io.Writer {
	if env.Stderr == nil {
		return io.Discard
	}
	return env.Stderr
}

// DefaultEnv uses the process streams.
func DefaultEnv() Env { return Env{Stdout: os.Stdout, Stderr: os.Stderr} }
